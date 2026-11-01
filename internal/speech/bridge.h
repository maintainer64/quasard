// SPDX-License-Identifier: GPL-3.0-only
#ifndef QUASARD_SPEECH_H
#define QUASARD_SPEECH_H
#include <stddef.h>
#include <stdint.h>
typedef struct qs_engine qs_engine;
typedef struct qs_vad qs_vad;
qs_engine *qs_new(const char *model,const char *voice,const char *config,const char *espeak,char **error);
void qs_free(qs_engine *e);
void *qs_recognizer(qs_engine *e);
int qs_accept(void *r,const int16_t *pcm,int count);
const char *qs_result(void *r);
void qs_recognizer_free(void *r);
int qs_synth_start(qs_engine *e,const char *text);
int qs_synth_next(qs_engine *e,const float **samples,size_t *count,int *rate);
qs_vad *qs_vad_new(const char *model,char **error);
int qs_vad_feed(qs_vad *v,const int16_t *samples,float *probability,char **error);
void qs_vad_free(qs_vad *v);
#endif
