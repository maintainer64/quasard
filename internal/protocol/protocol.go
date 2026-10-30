// SPDX-License-Identifier: GPL-3.0-only
package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"time"
)

type Map = map[string]any

func ID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:16] + "-" + h[16:24] + "-" + h[24:]
}
func Directive(ns, name string, payload Map, ref string, stream uint32) Map {
	h := Map{"namespace": ns, "name": name, "messageId": ID()}
	if ref != "" {
		h["refMessageId"] = ref
	}
	if stream != 0 {
		h["streamId"] = stream
	}
	return Map{"directive": Map{"header": h, "payload": payload}}
}
func Control(stream uint32, action, reason int) Map {
	return Map{"streamcontrol": Map{"messageId": ID(), "reason": reason, "action": action, "streamId": stream}}
}
func Sync(ref, session string) Map {
	d := Directive("System", "SynchronizeStateResponse", Map{"SessionId": session}, ref, 0)
	delete(d["directive"].(Map)["header"].(Map), "messageId")
	return d
}
func Push(ref string) Map {
	ack := time.Now().UnixMilli()
	d := Directive("System", "Push", Map{"analytics_meta_info": Map{"request_id": fmt.Sprintf("%d$%s$%s", ack*1000, ref, ID())}, "directives": []any{Map{"name": "notify", "payload": Map{"version_id": "0"}}}}, "", 0)
	d["directive"].(Map)["header"].(Map)["ack"] = ack
	return d
}
func InputAck(ref string) Map {
	return Directive("System", "InputStartAck", Map{"input_start_ack": Map{"request_start_time": time.Now().UnixMicro()}}, ref, 0)
}
func ASRResult(ref, text string, partial int, end bool, duration int) Map {
	words := []any{}
	for _, w := range strings.Fields(text) {
		words = append(words, Map{"value": w, "confidence": 1})
	}
	confidence := -.5
	if text == "" {
		confidence = -.001
	}
	meta := Map{}
	if end {
		meta = Map{"version": "aqw", "topic": "gigaam", "serverHostname": "local", "serverVersion": "1"}
	}
	d := Directive("ASR", "Result", Map{"recognition": []any{Map{"normalized": text, "words": words, "confidence": confidence, "parentModel": ""}}, "messagesCount": 1, "metainfo": meta, "responseCode": "OK", "doNotSendToClient": false, "asr_partial_number": partial, "endOfUtt": end, "bioResult": []any{}, "contextRef": []any{}, "durationProcessedAudio": fmt.Sprint(duration), "whisperInfo": Map{"isWhisper": false}}, ref, 0)
	d["directive"].(Map)["header"].(Map)["eventId"] = ref
	return d
}
func Validation(ref, device string, valid bool) Map {
	v := 0
	if valid {
		v = 1
	}
	return Directive("Spotter", "Validation", Map{"allow_activation_by_unvalidated_spotter": 0, "multiactivation_id": fmt.Sprint(time.Now().UnixMilli()) + device, "valid": v, "result": v, "canceled_cause_of_multiactivation": 0}, ref, 0)
}
func Action(name string, payload Map) Map {
	if payload == nil {
		payload = Map{}
	}
	return Map{"name": name, "payload": payload, "type": "client_action"}
}
func Vins(ref, request, session string, sequence int, text, speech string, actions []Map, listen bool) Map {
	dirs := append([]Map{}, actions...)
	hasVoice := speech != ""
	resp := Map{"experiments": Map{}, "directives_execution_policy": "BeforeSpeech"}
	if text != "" {
		card := Map{"card_id": ID(), "type": "simple_text", "text": text}
		resp["card"] = card
		resp["cards"] = []any{card}
	}
	if hasVoice {
		dirs = append([]Map{{"name": "tts_play_placeholder", "payload": Map{"channel": "Dialog"}, "sub_name": "tts_play_placeholder", "type": "client_action", "is_led_silent": true}}, dirs...)
	}
	resp["directives"] = dirs
	voice := Map{"has_voice_response": hasVoice, "tts_voice_settings": Map{"effective_language": "L_RUS", "main_request_language": "L_RUS"}, "should_listen": listen}
	if hasVoice {
		voice["output_speech"] = Map{"type": "simple", "text": speech}
	}
	return Directive("Vins", "VinsResponse", Map{"version": "aqw", "header": Map{"response_id": ID(), "ref_message_id": ref, "request_id": request, "sequence_number": sequence, "parent_request_id": request, "session_id": session, "dialog_id": nil}, "response": resp, "voice_response": voice}, ref, 0)
}
func Speak(ref string, stream uint32) Map {
	return Directive("TTS", "Speak", Map{"from_cache": false, "enable_bargin": false, "lazy_tts_streaming": false, "disableInterruptionSpotter": false, "format": "audio/opus"}, ref, stream)
}
