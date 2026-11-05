// SPDX-License-Identifier: GPL-3.0-only
#ifndef QUASARD_SPEECH_H
#define QUASARD_SPEECH_H
#include <stddef.h>
#include <stdint.h>
#ifdef __cplusplus
extern "C" {
#endif
typedef struct qs_engine qs_engine;
typedef struct qs_gigaam qs_gigaam;
typedef struct qs_gigaam_result {
 int32_t *tokens;
 int count;
 int frames;
 double frontend_ms;
 double encoder_ms;
 double decoder_ms;
} qs_gigaam_result;
qs_gigaam *qs_gigaam_new(const char *encoder,const char *decoder,const char *joiner,const float *mel,int threads,char **error);
void qs_gigaam_free(qs_gigaam *g);
void qs_gigaam_cancel(qs_gigaam *g);
int qs_gigaam_reset(qs_gigaam *g,char **error);
int qs_gigaam_transcribe(qs_gigaam *g,const int16_t *pcm,int count,qs_gigaam_result *result,char **error);
typedef struct qs_vad qs_vad;
void *qs_create_piper(const char *voice,const char *config,const char *espeak,char **error);
qs_engine *qs_new(const char *voice,const char *config,const char *espeak,char **error);
int qs_add_english_voice(qs_engine *e,const char *voice,const char *config,const char *espeak,char **error);
void qs_free(qs_engine *e);
int qs_synth_start(qs_engine *e,const char *text,int english);
int qs_synth_next(qs_engine *e,const float **samples,size_t *count,int *rate);
qs_vad *qs_vad_new(const char *model,char **error);
int qs_vad_feed(qs_vad *v,const int16_t *samples,float *probability,char **error);
void qs_vad_free(qs_vad *v);
#ifdef __cplusplus
}
#endif
#endif
