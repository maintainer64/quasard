//go:build native && cgo && linux

// SPDX-License-Identifier: GPL-3.0-only
#include "bridge.h"
#include <stdlib.h>
#include <string.h>
#include <stdio.h>
#include <vosk_api.h>
#include <piper.h>
#include <onnxruntime_c_api.h>

struct qs_engine { VoskModel *model; piper_synthesizer *piper; };
qs_engine *qs_new(const char *model,const char *voice,const char *config,const char *espeak,char **error) {
 qs_engine *e=calloc(1,sizeof(*e)); if(!e){*error=strdup("allocation failed");return NULL;}
 vosk_set_log_level(-1);e->model=vosk_model_new(model);
 if(!e->model){*error=strdup("Vosk model load failed");qs_free(e);return NULL;}
 e->piper=piper_create(voice,config,espeak);
 if(!e->piper){*error=strdup("Piper model load failed");qs_free(e);return NULL;}return e;
}
void qs_free(qs_engine *e){if(!e)return;if(e->piper)piper_free(e->piper);if(e->model)vosk_model_free(e->model);free(e);}
void *qs_recognizer(qs_engine *e){return vosk_recognizer_new(e->model,16000.0f);}
int qs_accept(void *r,const int16_t *pcm,int count){return vosk_recognizer_accept_waveform_s(r,pcm,count);}
const char *qs_result(void *r){return vosk_recognizer_final_result(r);}
void qs_recognizer_free(void *r){vosk_recognizer_free(r);}
int qs_synth_start(qs_engine *e,const char *text){return piper_synthesize_start(e->piper,text,NULL);}
int qs_synth_next(qs_engine *e,const float **samples,size_t *count,int *rate){piper_audio_chunk chunk={0};int result=piper_synthesize_next(e->piper,&chunk);*samples=chunk.samples;*count=chunk.num_samples;*rate=chunk.sample_rate;return result;}

struct qs_vad {const OrtApi *api;OrtEnv *env;OrtSession *session;OrtMemoryInfo *memory;float state[256];float context[64];};
static int check(qs_vad *v,OrtStatus *s,char **error){if(!s)return 0;*error=strdup(v->api->GetErrorMessage(s));v->api->ReleaseStatus(s);return -1;}
qs_vad *qs_vad_new(const char *model,char **error){
 qs_vad *v=calloc(1,sizeof(*v));if(!v){*error=strdup("VAD allocation failed");return NULL;}v->api=OrtGetApiBase()->GetApi(ORT_API_VERSION);if(!v->api){*error=strdup("ONNX Runtime API mismatch");free(v);return NULL;}
 OrtSessionOptions *opts=NULL;
 if(check(v,v->api->CreateEnv(ORT_LOGGING_LEVEL_WARNING,"quasard",&v->env),error)||check(v,v->api->CreateSessionOptions(&opts),error))goto fail;
 if(check(v,v->api->SetIntraOpNumThreads(opts,1),error)||check(v,v->api->SetInterOpNumThreads(opts,1),error)||check(v,v->api->CreateSession(v->env,model,opts,&v->session),error))goto fail;
 v->api->ReleaseSessionOptions(opts);opts=NULL;
 if(check(v,v->api->CreateCpuMemoryInfo(OrtArenaAllocator,OrtMemTypeDefault,&v->memory),error))goto fail;
 return v;
fail: if(opts)v->api->ReleaseSessionOptions(opts);qs_vad_free(v);return NULL;
}
int qs_vad_feed(qs_vad *v,const int16_t *samples,float *probability,char **error){
 float input[576];memcpy(input,v->context,sizeof(v->context));for(int i=0;i<512;i++)input[64+i]=samples[i]/32768.0f;
 int64_t dims_input[2]={1,576},dims_state[3]={1,1,128};
 OrtValue *inputs[3]={NULL,NULL,NULL},*outputs[3]={NULL,NULL,NULL};
 const char *in_names[]={"input","h","c"};const char *out_names[]={"speech_probs","hn","cn"};int result=-1;
 if(check(v,v->api->CreateTensorWithDataAsOrtValue(v->memory,input,sizeof(input),dims_input,2,ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT,&inputs[0]),error)||check(v,v->api->CreateTensorWithDataAsOrtValue(v->memory,v->state,128*sizeof(float),dims_state,3,ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT,&inputs[1]),error)||check(v,v->api->CreateTensorWithDataAsOrtValue(v->memory,v->state+128,128*sizeof(float),dims_state,3,ONNX_TENSOR_ELEMENT_DATA_TYPE_FLOAT,&inputs[2]),error))goto done;
 if(check(v,v->api->Run(v->session,NULL,in_names,(const OrtValue *const*)inputs,3,out_names,3,outputs),error))goto done;
 float *p=NULL,*h=NULL,*c=NULL;
 if(check(v,v->api->GetTensorMutableData(outputs[0],(void**)&p),error)||check(v,v->api->GetTensorMutableData(outputs[1],(void**)&h),error)||check(v,v->api->GetTensorMutableData(outputs[2],(void**)&c),error))goto done;
 *probability=p[0];memcpy(v->state,h,128*sizeof(float));memcpy(v->state+128,c,128*sizeof(float));memcpy(v->context,input+512,sizeof(v->context));result=0;
done:for(int i=0;i<3;i++){if(inputs[i])v->api->ReleaseValue(inputs[i]);if(outputs[i])v->api->ReleaseValue(outputs[i]);}return result;
}
void qs_vad_free(qs_vad *v){if(!v)return;if(v->memory)v->api->ReleaseMemoryInfo(v->memory);if(v->session)v->api->ReleaseSession(v->session);if(v->env)v->api->ReleaseEnv(v->env);free(v);}
