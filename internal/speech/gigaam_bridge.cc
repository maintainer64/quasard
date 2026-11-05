//go:build native && cgo && linux

// SPDX-License-Identifier: GPL-3.0-only
#include "bridge.h"
#include <onnxruntime_cxx_api.h>
#include <algorithm>
#include <atomic>
#include <chrono>
#include <cmath>
#include <cstdlib>
#include <cstring>
#include <stdexcept>
#include <vector>

namespace {
constexpr int fft_size=320, hop=160, bins=161, mels=64;
constexpr int encoder_dim=768, hidden_size=320, layers=1, blank=1024, max_symbols=10;
using Clock=std::chrono::steady_clock;
double elapsed(Clock::time_point a,Clock::time_point b) {
 return std::chrono::duration<double,std::milli>(b-a).count();
}
void shape(const Ort::Value &value,std::initializer_list<int64_t> expected,
 ONNXTensorElementDataType type=ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT) {
 auto info=value.GetTensorTypeAndShapeInfo();
 if(info.GetElementType()!=type)throw std::runtime_error("GigaAM tensor type mismatch");
 auto actual=info.GetShape();
 if(actual.size()!=expected.size())throw std::runtime_error("GigaAM tensor rank mismatch");
 size_t i=0;for(auto dimension:expected) {
  if(dimension>=0 && actual[i]!=dimension)throw std::runtime_error("GigaAM tensor shape mismatch");
  ++i;
 }
}
}

struct qs_gigaam {
 Ort::Env env{ORT_LOGGING_LEVEL_ERROR,"quasard-gigaam"};
 Ort::SessionOptions options;
 Ort::RunOptions run;
 Ort::MemoryInfo memory=Ort::MemoryInfo::CreateCpu(OrtArenaAllocator,OrtMemTypeDefault);
 Ort::Session encoder{nullptr},decoder{nullptr},joiner{nullptr};
 std::atomic<bool> cancelled{false};
 std::vector<float> mel;
 std::vector<double> real_basis,imag_basis;

 qs_gigaam(const char *enc,const char *dec,const char *joint,const float *filters,int threads)
  :mel(filters,filters+mels*bins),real_basis(bins*fft_size),imag_basis(bins*fft_size) {
  options.SetIntraOpNumThreads(threads);
  options.SetInterOpNumThreads(1);
  options.SetExecutionMode(ExecutionMode::ORT_SEQUENTIAL);
  options.SetGraphOptimizationLevel(GraphOptimizationLevel::ORT_ENABLE_EXTENDED);
  options.AddConfigEntry("session.intra_op.allow_spinning","0");
  options.AddConfigEntry("session.inter_op.allow_spinning","0");
  // Avoid retaining a large arena between utterances alongside resident TTS.
  options.DisableCpuMemArena();
  encoder=Ort::Session(env,enc,options);
  decoder=Ort::Session(env,dec,options);
  joiner=Ort::Session(env,joint,options);
  const double pi=std::acos(-1.0);
  for(int k=0;k<bins;k++)for(int n=0;n<fft_size;n++) {
   const double window=0.5-0.5*std::cos(2*pi*n/fft_size); // periodic Hann
   const double angle=2*pi*k*n/fft_size;
   real_basis[k*fft_size+n]=window*std::cos(angle);
   imag_basis[k*fft_size+n]=-window*std::sin(angle);
  }
 }
 void check_cancelled() const {
  if(cancelled.load(std::memory_order_relaxed))throw std::runtime_error("GigaAM cancelled");
 }
 std::vector<float> features(const int16_t *pcm,int count,int frames) {
  std::vector<float> result(mels*frames);
  for(int t=0;t<frames;t++) {
   check_cancelled();
   float power[bins];
   for(int k=0;k<bins;k++) {
    double re=0,im=0;
    for(int n=0;n<fft_size;n++) {
     const double sample=pcm[t*hop+n]/32768.0;
     re+=sample*real_basis[k*fft_size+n];
     im+=sample*imag_basis[k*fft_size+n];
    }
    power[k]=static_cast<float>(re*re+im*im);
   }
   for(int m=0;m<mels;m++) {
    float energy=0;
    for(int k=0;k<bins;k++)energy+=power[k]*mel[m*bins+k];
    result[m*frames+t]=std::log(std::clamp(energy,1e-9f,1e9f));
   }
  }
  return result;
 }
};

extern "C" qs_gigaam *qs_gigaam_new(const char *encoder,const char *decoder,const char *joiner,
 const float *mel,int threads,char **error) {
 try{return new qs_gigaam(encoder,decoder,joiner,mel,threads);}
 catch(const std::exception &e){*error=strdup(e.what());return nullptr;}
 catch(...){*error=strdup("GigaAM initialization failed");return nullptr;}
}
extern "C" void qs_gigaam_free(qs_gigaam *g){delete g;}
extern "C" void qs_gigaam_cancel(qs_gigaam *g) {
 g->cancelled.store(true,std::memory_order_relaxed);
 try{g->run.SetTerminate();}catch(...){}
}
extern "C" int qs_gigaam_reset(qs_gigaam *g,char **error) {
 try{g->run.UnsetTerminate();g->cancelled.store(false,std::memory_order_relaxed);return 0;}
 catch(const std::exception &e){*error=strdup(e.what());return -1;}
}
extern "C" int qs_gigaam_transcribe(qs_gigaam *g,const int16_t *pcm,int count,
 qs_gigaam_result *result,char **error) {
 *result={};
 try {
  if(count<fft_size)return 0;
  if(count>25*16000)throw std::runtime_error("GigaAM utterance exceeds 25 seconds");
  const auto start=Clock::now();
  int64_t length=(count-fft_size)/hop+1;
  auto features=g->features(pcm,count,static_cast<int>(length));
  const auto ready=Clock::now();
  int64_t dims_features[]={1,mels,length},dims_length[]={1};
  Ort::Value enc_inputs[]={
   Ort::Value::CreateTensor<float>(g->memory,features.data(),features.size(),dims_features,3),
   Ort::Value::CreateTensor<int64_t>(g->memory,&length,1,dims_length,1)};
  const char *enc_in[]={"audio_signal","length"},*enc_out[]={"encoded","encoded_len"};
  auto encoded=g->encoder.Run(g->run,enc_in,enc_inputs,2,enc_out,2);
  shape(encoded[0],{1,encoder_dim,-1});shape(encoded[1],{1},ONNX_TENSOR_ELEMENT_DATA_TYPE_INT32);
  auto enc_shape=encoded[0].GetTensorTypeAndShapeInfo().GetShape();
  int64_t steps=encoded[1].GetTensorData<int32_t>()[0],stride=enc_shape[2];
  if(steps<0 || stride<0 || stride>length)throw std::runtime_error("Invalid GigaAM encoder length");
  // Upstream RNNT iterates range(T) and tests t < encoded_len. Some input
  // lengths round encoded_len one frame above T; preserve the same clamp.
  steps=std::min(steps,stride);
  const float *enc_data=encoded[0].GetTensorData<float>();
  const auto encoder_done=Clock::now();
  std::vector<float> h(hidden_size*layers,0),c(hidden_size*layers,0),frame(encoder_dim);
  std::vector<int32_t> tokens;
  int64_t label=blank;
  int64_t dims_label[]={1,1},dims_state[]={layers,1,hidden_size};
  int64_t dims_enc[]={1,encoder_dim,1},dims_dec[]={1,hidden_size,1};
  const char *pred_in[]={"x","hi","ci"},*pred_out[]={"dec","ho","co"};
  const char *joint_in[]={"enc","dec"},*joint_out[]={"joint"};
  std::vector<Ort::Value> prediction;
  bool prediction_dirty=true;
  for(int64_t t=0;t<steps;t++) {
   g->check_cancelled();
   for(int d=0;d<encoder_dim;d++)frame[d]=enc_data[d*stride+t];
   for(int symbol=0;symbol<max_symbols;symbol++) {
    g->check_cancelled();
    if(prediction_dirty) {
     Ort::Value pred_inputs[]={
      Ort::Value::CreateTensor<int64_t>(g->memory,&label,1,dims_label,2),
      Ort::Value::CreateTensor<float>(g->memory,h.data(),h.size(),dims_state,3),
      Ort::Value::CreateTensor<float>(g->memory,c.data(),c.size(),dims_state,3)};
     prediction=g->decoder.Run(g->run,pred_in,pred_inputs,3,pred_out,3);
     shape(prediction[0],{1,1,hidden_size});
     shape(prediction[1],{layers,1,hidden_size});shape(prediction[2],{layers,1,hidden_size});
     prediction_dirty=false;
    }
    // A one-token [1,1,H] prediction has the same storage after transpose.
    Ort::Value joint_inputs[]={
     Ort::Value::CreateTensor<float>(g->memory,frame.data(),frame.size(),dims_enc,3),
     Ort::Value::CreateTensor<float>(g->memory,prediction[0].GetTensorMutableData<float>(),hidden_size,dims_dec,3)};
    auto joint=g->joiner.Run(g->run,joint_in,joint_inputs,2,joint_out,1);
    shape(joint[0],{1,1,1,blank+1});
    const float *scores=joint[0].GetTensorData<float>();
    int best=0;
    for(int token=0;token<=blank;token++) {
     if(!std::isfinite(scores[token]))throw std::runtime_error("Nonfinite GigaAM logits");
     if(scores[token]>scores[best])best=token;
    }
    if(best==blank)break;
    tokens.push_back(best);label=best;
    std::copy_n(prediction[1].GetTensorData<float>(),h.size(),h.begin());
    std::copy_n(prediction[2].GetTensorData<float>(),c.size(),c.begin());
    prediction_dirty=true;
   }
  }
  g->check_cancelled();
  if(!tokens.empty()) {
   result->tokens=static_cast<int32_t *>(std::malloc(tokens.size()*sizeof(int32_t)));
   if(!result->tokens)throw std::bad_alloc();
   std::copy(tokens.begin(),tokens.end(),result->tokens);
  }
  result->count=static_cast<int>(tokens.size());result->frames=static_cast<int>(steps);
  result->frontend_ms=elapsed(start,ready);result->encoder_ms=elapsed(ready,encoder_done);
  result->decoder_ms=elapsed(encoder_done,Clock::now());
  return 0;
 }catch(const std::exception &e){*error=strdup(e.what());return -1;}
 catch(...){*error=strdup("GigaAM inference failed");return -1;}
}
