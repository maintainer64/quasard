// SPDX-License-Identifier: GPL-3.0-only
#ifndef QUASARD_SPEECH_H
#define QUASARD_SPEECH_H
#include <stddef.h>
#include <stdint.h>
#ifdef __cplusplus
extern "C" {
#endif
typedef struct qs_engine qs_engine;
typedef struct qs_whisper qs_whisper;
qs_whisper *qs_whisper_new(const char *model,const char *language,int threads,char **error);
void qs_whisper_free(qs_whisper *w);
void qs_whisper_cancel(qs_whisper *w);
void qs_whisper_reset(qs_whisper *w);
typedef struct qs_whisper_result {
 char *text;
 const char *language;
 int automatic;
 float ru_probability;
 float en_probability;
} qs_whisper_result;
int qs_whisper_transcribe(qs_whisper *w,const int16_t *pcm,int count,qs_whisper_result *result);
typedef struct qs_vad qs_vad;
void *qs_create_piper(const char *voice,const char *config,const char *espeak,char **error);
qs_engine *qs_new(const char *model,const char *voice,const char *config,const char *espeak,char **error);
int qs_add_english_voice(qs_engine *e,const char *voice,const char *config,const char *espeak,char **error);
void qs_free(qs_engine *e);
void *qs_recognizer(qs_engine *e);
int qs_accept(void *r,const int16_t *pcm,int count);
const char *qs_result(void *r);
void qs_recognizer_free(void *r);
int qs_synth_start(qs_engine *e,const char *text,int english);
int qs_synth_next(qs_engine *e,const float **samples,size_t *count,int *rate);
qs_vad *qs_vad_new(const char *model,char **error);
int qs_vad_feed(qs_vad *v,const int16_t *samples,float *probability,char **error);
void qs_vad_free(qs_vad *v);
#ifdef __cplusplus
}
#endif
#endif
