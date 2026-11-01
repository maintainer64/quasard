// SPDX-License-Identifier: GPL-3.0-only
package aliceapi

func integer(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case uint32:
		return int(x)
	case string:
		var n int
		for _, c := range x {
			if c < '0' || c > '9' {
				return 0
			}
			n = n*10 + int(c-'0')
		}
		return n
	}
	return 0
}
func array(v any) []any {
	a, _ := v.([]any)
	if a != nil {
		return a
	}
	if maps, ok := v.([]map[string]any); ok {
		for _, m := range maps {
			a = append(a, m)
		}
	}
	return a
}
func ClientToJSON(m Map, ref string) Map {
	if raw, ok := m["StreamControl"]; ok {
		sc := obj(raw)
		action, reason := 0, 0
		if _, ok := sc["SpotterEnd"]; ok {
			action = 2
		} else if close, ok := sc["Close"]; ok {
			reason = map[string]int{"OK": 0, "INTERNAL_ERROR": 2, "STOP_RECORDING": 5, "AUDIO_SOURCE_STOPPED": 6, "CANCEL": 7, "NO_SPEECH": 8}[str(obj(close)["Reason"])]
		} else {
			return nil
		}
		return Map{"streamcontrol": Map{"action": action, "reason": reason, "streamId": integer(sc["StreamId"]), "messageId": sc["MessageId"]}}
	}
	ev := obj(m["Event"])
	if len(ev) == 0 {
		return nil
	}
	h := obj(ev["Header"])
	kind := ""
	for k := range ev {
		if k != "Header" {
			kind = k
			break
		}
	}
	body := obj(ev[kind])
	names := map[string][2]string{"SynchronizeStateRequest": {"System", "SynchronizeState"}, "VoiceInput": {"Vins", "VoiceInput"}, "TextInput": {"Vins", "TextInput"}, "LogSpotter": {"Log", "Spotter"}, "RequestStat": {"Log", "RequestStat"}, "Ack": {"System", "Ack"}, "Ping": {"System", "Ping"}, "CancelRequest": {"Vins", "CancelRequest"}}
	nsname, ok := names[kind]
	if !ok {
		nsname = [2]string{"Proto", kind}
	}
	header := Map{"namespace": nsname[0], "name": nsname[1], "messageId": h["MessageId"], "refMessageId": h["RefMessageId"]}
	if id, ok := h["StreamId"]; ok {
		header["streamId"] = integer(id)
	}
	payload := Map{"proto": body}
	if kind == "VoiceInput" || kind == "TextInput" {
		rh := obj(body["Header"])
		ev := obj(obj(body["Request"])["Event"])
		typ := ev["Type"]
		if kind == "TextInput" && (typ == nil || typ == "text_input") {
			typ = "text_input"
		}
		payload = Map{"header": Map{"request_id": rh["RequestId"], "sequence_number": integer(rh["SequenceNumber"])}, "enable_spotter_validation": body["EnableSpotterValidation"] == true, "request": Map{"event": Map{"type": typ, "text": ev["Text"], "name": ev["Name"], "payload": obj(ev["Payload"])}}}
	}
	return Map{"event": Map{"header": header, "payload": payload}}
}
func protoDirective(v any) Map {
	d := obj(v)
	typ := str(d["type"])
	if typ == "" {
		typ = "client_action"
	}
	sub := str(d["sub_name"])
	if sub == "" {
		sub = str(d["name"])
	}
	m := Map{"Type": typ, "Name": str(d["name"]), "AnalyticsType": sub, "Payload": obj(d["payload"])}
	if d["is_led_silent"] == true {
		m["IsLedSilent"] = true
	}
	return m
}
func ServerMessage(msg Map) Map {
	if raw, ok := msg["streamcontrol"]; ok {
		sc := obj(raw)
		r := map[int]string{0: "OK", 2: "INTERNAL_ERROR", 5: "STOP_RECORDING", 6: "AUDIO_SOURCE_STOPPED", 7: "CANCEL", 8: "NO_SPEECH"}[integer(sc["reason"])]
		if r == "" {
			r = "OK"
		}
		return Map{"StreamControl": Map{"StreamId": sc["streamId"], "MessageId": sc["messageId"], "Close": Map{"Reason": r}}}
	}
	d := obj(msg["directive"])
	h, p := obj(d["header"]), obj(d["payload"])
	header := Map{"MessageId": str(h["messageId"]), "RefMessageId": str(h["refMessageId"])}
	if id, ok := h["streamId"]; ok {
		header["StreamId"] = id
	}
	body := Map{}
	switch str(h["namespace"]) + "." + str(h["name"]) {
	case "System.SynchronizeStateResponse":
		body["SynchronizeStateResponse"] = Map{"SessionId": p["SessionId"]}
	case "System.Push":
		header["Ack"] = h["ack"]
		dirs := []any{}
		for _, v := range array(p["directives"]) {
			dirs = append(dirs, protoDirective(v))
		}
		body["Push"] = Map{"Directives": dirs}
	case "System.InputStartAck":
		body["InputStartAck"] = Map{"RequestStartTime": obj(p["input_start_ack"])["request_start_time"]}
	case "ASR.Result":
		rec := []any{}
		for _, v := range array(p["recognition"]) {
			r := obj(v)
			if str(r["normalized"]) == "" {
				continue
			}
			words := []any{}
			for _, w := range array(r["words"]) {
				words = append(words, Map{"Value": obj(w)["value"], "Confidence": .999})
			}
			rec = append(rec, Map{"Normalized": r["normalized"], "Confidence": .999, "Words": words})
		}
		body["AsrResult"] = Map{"EndOfUtt": p["endOfUtt"], "MessagesCount": 1, "Recognition": rec}
	case "Spotter.Validation":
		res := "SpottingRejected"
		if integer(p["valid"]) != 0 {
			res = "Ok"
		}
		body["SpotterValidation"] = Map{"Result": res}
	case "Log.Ack":
		body["LogAck"] = Map{}
	case "Log.RequestStatAck":
		body["RequestStatAck"] = Map{}
	case "TTS.Speak":
		body["TtsSpeak"] = Map{"Format": p["format"], "LazyTtsStreaming": false}
	case "Vins.VinsResponse":
		r, vr, ph := obj(p["response"]), obj(p["voice_response"]), obj(p["header"])
		voice := Map{"HasVoiceResponse": vr["has_voice_response"] == true, "ShouldListen": vr["should_listen"] == true}
		if speech := obj(vr["output_speech"]); len(speech) > 0 {
			voice["OutputSpeech"] = Map{"Text": speech["text"]}
		}
		cards, dirs := []any{}, []any{}
		for _, v := range array(r["cards"]) {
			c := obj(v)
			cards = append(cards, Map{"Type": c["type"], "Text": c["text"], "CardId": c["card_id"]})
		}
		for _, v := range array(r["directives"]) {
			dirs = append(dirs, protoDirective(v))
		}
		body["AliceResponse"] = Map{"Header": Map{"RequestId": str(ph["request_id"]), "ResponseId": str(ph["response_id"]), "SequenceNumber": integer(ph["sequence_number"]), "ParentRequestId": str(ph["request_id"])}, "Response": Map{"Cards": cards, "Directives": dirs}, "VoiceResponse": voice}
	default:
		return nil
	}
	body["Header"] = header
	return Map{"Event": body}
}
