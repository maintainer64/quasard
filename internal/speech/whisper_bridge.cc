//go:build native && cgo && linux

// SPDX-License-Identifier: GPL-3.0-only
#include "bridge.h"
#include <whisper.h>
#include <ggml-backend.h>
#include <atomic>
#include <cstdlib>
#include <cmath>
#include <cstring>
#include <new>
#include <string>
#include <vector>

struct qs_whisper {
 whisper_context *ctx = nullptr;
 std::atomic<bool> cancelled{false};
 int threads = 2;
 std::string language;
};
static void quiet_whisper(ggml_log_level, const char *, void *) {}
static bool whisper_cancelled(void *opaque) {
 return static_cast<qs_whisper *>(opaque)->cancelled.load(std::memory_order_relaxed);
}
extern "C" qs_whisper *qs_whisper_new(const char *model, const char *language, int threads, char **error) {
 qs_whisper *w = nullptr;
 try {
  w = new qs_whisper();
  w->threads = threads;
  w->language = language;
  if(w->language != "auto" && whisper_lang_id(language) < 0) {
   *error=strdup("Unsupported Whisper language");delete w;return nullptr;
  }
  whisper_log_set(quiet_whisper,nullptr);
  ggml_backend_load_all_from_path("/opt/quasard/lib");
  auto params=whisper_context_default_params();
  params.use_gpu=false;
  w->ctx=whisper_init_from_file_with_params(model,params);
  if(!w->ctx) { *error=strdup("Whisper model load failed");delete w;return nullptr; }
  if(w->language=="auto" && !whisper_is_multilingual(w->ctx)) {
   *error=strdup("Automatic language detection requires a multilingual Whisper model");
   whisper_free(w->ctx);delete w;return nullptr;
  }
  return w;
 } catch(...) {
  if(w) { if(w->ctx)whisper_free(w->ctx);delete w; }
  *error=strdup("Whisper initialization failed");return nullptr;
 }
}
extern "C" void qs_whisper_free(qs_whisper *w) {
 if(w) { whisper_free(w->ctx);delete w; }
}
extern "C" void qs_whisper_cancel(qs_whisper *w) { w->cancelled.store(true,std::memory_order_relaxed); }
extern "C" void qs_whisper_reset(qs_whisper *w) { w->cancelled.store(false,std::memory_order_relaxed); }
extern "C" int qs_whisper_transcribe(qs_whisper *w,const int16_t *pcm,int count,qs_whisper_result *result) {
 *result={};
 try {
  std::vector<float> samples(count);
  for(int i=0;i<count;i++)samples[i]=static_cast<float>(pcm[i])/32768.0f;
  auto p=whisper_full_default_params(WHISPER_SAMPLING_GREEDY);
  p.n_threads=w->threads;
  p.translate=false;
  p.detect_language=false;
  p.no_context=true;
  p.language=w->language.c_str();
  p.print_realtime=false;
  p.print_progress=false;
  p.print_timestamps=false;
  p.print_special=false;
  p.abort_callback=whisper_cancelled;
  p.abort_callback_user_data=w;
  const float *input=samples.data();
  int input_count=count;
  if(w->language=="auto") {
   // Detection-only keeps the abort callback active during both native graphs.
   // Its final logits are language-token scores, before any text decoding.
   p.detect_language=true;
   int status=whisper_full(w->ctx,p,input,input_count);
   if(status!=0)return status;
   const float *logits=whisper_get_logits(w->ctx);
   if(!logits)return -1;
   const float ru=logits[whisper_token_lang(w->ctx,whisper_lang_id("ru"))];
   const float en=logits[whisper_token_lang(w->ctx,whisper_lang_id("en"))];
   if(!std::isfinite(ru) || !std::isfinite(en))return -1;
   const float maximum=ru>en?ru:en;
   const float ru_score=std::exp(ru-maximum);
   const float en_score=std::exp(en-maximum);
   result->automatic=1;
   // Conditional probabilities within RU/EN, not calibrated ASR confidence.
   result->ru_probability=ru_score/(ru_score+en_score);
   result->en_probability=en_score/(ru_score+en_score);
   p.language=en>ru?"en":"ru";
   p.detect_language=false;
   // Reuse this utterance's mel spectrogram; normal auto also runs an encoder
   // for detection and another for transcription. No second ASR/model pass.
   input=nullptr;
   input_count=0;
  }
  result->language=p.language;
  int status=whisper_full(w->ctx,p,input,input_count);
  if(status!=0)return status;
  std::string text;
  for(int i=0;i<whisper_full_n_segments(w->ctx);i++)text+=whisper_full_get_segment_text(w->ctx,i);
  result->text=strdup(text.c_str());
  return result->text?0:-1;
 }catch(...){return -1;}
}
