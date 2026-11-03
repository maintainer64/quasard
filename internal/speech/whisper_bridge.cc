//go:build native && cgo && linux

// SPDX-License-Identifier: GPL-3.0-only
#include "bridge.h"
#include <whisper.h>
#include <ggml-backend.h>
#include <atomic>
#include <cstdlib>
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
extern "C" int qs_whisper_transcribe(qs_whisper *w,const int16_t *pcm,int count,char **text) {
 *text=nullptr;
 try {
  std::vector<float> samples(count);
  for(int i=0;i<count;i++)samples[i]=static_cast<float>(pcm[i])/32768.0f;
  auto p=whisper_full_default_params(WHISPER_SAMPLING_GREEDY);
  p.n_threads=w->threads;
  p.translate=false;
  p.detect_language=false; // Transcribe after auto-detection; do not stop at detection.
  p.no_context=true;
  p.language=w->language.c_str();
  p.print_realtime=false;
  p.print_progress=false;
  p.print_timestamps=false;
  p.print_special=false;
  p.abort_callback=whisper_cancelled;
  p.abort_callback_user_data=w;
  int status=whisper_full(w->ctx,p,samples.data(),count);
  if(status!=0)return status;
  std::string result;
  for(int i=0;i<whisper_full_n_segments(w->ctx);i++)result+=whisper_full_get_segment_text(w->ctx,i);
  *text=strdup(result.c_str());
  return *text?0:-1;
 }catch(...){return -1;}
}
