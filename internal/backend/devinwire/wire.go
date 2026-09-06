// Package devinwire implements the small reviewed subset of Devin's
// Codeium/Cascade protobuf protocol that Routsi needs. Field numbers are from
// the exa.* protobuf descriptors used by Devin CLI; unknown fields are ignored.
package devinwire

import (
	"encoding/binary"
	"errors"
	"math"

	"google.golang.org/protobuf/encoding/protowire"
)

func str(b []byte, n protowire.Number, v string) []byte {
	if v == "" {
		return b
	}
	b = protowire.AppendTag(b, n, protowire.BytesType)
	return protowire.AppendString(b, v)
}
func msg(b []byte, n protowire.Number, v []byte) []byte {
	if len(v) == 0 {
		return b
	}
	b = protowire.AppendTag(b, n, protowire.BytesType)
	return protowire.AppendBytes(b, v)
}
func vint(b []byte, n protowire.Number, v uint64) []byte {
	if v == 0 {
		return b
	}
	b = protowire.AppendTag(b, n, protowire.VarintType)
	return protowire.AppendVarint(b, v)
}
func boolean(b []byte, n protowire.Number, v bool) []byte {
	if !v {
		return b
	}
	return vint(b, n, 1)
}
func dbl(b []byte, n protowire.Number, v float64) []byte {
	if v == 0 {
		return b
	}
	b = protowire.AppendTag(b, n, protowire.Fixed64Type)
	return protowire.AppendFixed64(b, math.Float64bits(v))
}

type Metadata struct {
	APIKey, UserJWT string
	Discovery       bool
}

func (m Metadata) Marshal() []byte {
	ideName, ideVersion := "devin-cli", "3000.6.2"
	if m.Discovery {
		ideName, ideVersion = "chisel", "0.0.0-dev"
	}
	b := str(nil, 1, ideName)
	b = str(b, 2, ideVersion)
	b = str(b, 3, normalizeToken(m.APIKey))
	b = str(b, 4, "en")
	b = str(b, 5, runtimeOS())
	b = str(b, 7, ideVersion)
	b = str(b, 12, "chisel")
	b = str(b, 21, m.UserJWT)
	b = str(b, 28, "chisel")
	if m.Discovery {
		for _, display := range []uint64{3, 4, 6, 7, 8} {
			b = vint(b, 30, display)
		}
	}
	return b
}

func normalizeToken(v string) string {
	const prefix = "devin-session-token$"
	if len(v) >= len(prefix) && v[:len(prefix)] == prefix {
		return v
	}
	return prefix + v
}

type Model struct {
	Label, UID       string
	Disabled, Router bool
}

func MarshalAuthRequest(meta Metadata) []byte { return msg(nil, 1, meta.Marshal()) }
func MarshalDiscoveryRequest(meta Metadata) []byte {
	meta.Discovery = true
	return msg(nil, 1, meta.Marshal())
}

func DecodeAuthResponse(b []byte) (jwt, baseURL string, err error) {
	fields, err := fields(b)
	if err != nil {
		return "", "", err
	}
	jwt = firstString(fields[1])
	baseURL = firstString(fields[2])
	if jwt == "" {
		err = errors.New("GetUserJwt returned no JWT")
	}
	return
}

func DecodeModels(b []byte) ([]Model, string, error) {
	f, err := fields(b)
	if err != nil {
		return nil, "", err
	}
	var out []Model
	for _, raw := range f[1] {
		mf, e := fields(raw)
		if e != nil {
			return nil, "", e
		}
		m := Model{Label: firstString(mf[1]), UID: firstString(mf[22]), Disabled: firstVarint(mf[4]) != 0}
		if infos := mf[23]; len(infos) > 0 {
			inf, _ := fields(infos[0])
			m.Router = firstVarint(inf[25]) != 0
		}
		if m.UID != "" {
			out = append(out, m)
		}
	}
	defaultUID := ""
	if defs := f[3]; len(defs) > 0 {
		df, _ := fields(defs[0])
		defaultUID = firstString(df[3])
	}
	return out, defaultUID, nil
}

type Prompt struct {
	ID               string
	Source           uint64
	Text, ToolCallID string
	ToolError        bool
	ToolCalls        []ToolCall
}
type Tool struct {
	Name, Description, JSONSchema string
	Strict                        bool
}
type ToolCall struct{ ID, Name, Arguments string }
type ChatRequest struct {
	Metadata                                                Metadata
	System, ModelUID, CascadeID, ExecutionID, AssignmentJWT string
	ToolChoice, ToolName                                    string
	Prompts                                                 []Prompt
	Tools                                                   []Tool
}

func marshalToolCall(t ToolCall) []byte {
	b := str(nil, 1, t.ID)
	b = str(b, 2, t.Name)
	return str(b, 3, t.Arguments)
}
func marshalPrompt(p Prompt) []byte {
	b := str(nil, 1, p.ID)
	b = vint(b, 2, p.Source)
	b = str(b, 3, p.Text)
	for _, t := range p.ToolCalls {
		b = msg(b, 6, marshalToolCall(t))
	}
	b = str(b, 7, p.ToolCallID)
	b = boolean(b, 9, p.ToolError)
	return b
}
func marshalTool(t Tool) []byte {
	b := str(nil, 1, t.Name)
	b = str(b, 2, t.Description)
	b = str(b, 3, t.JSONSchema)
	return boolean(b, 12, t.Strict)
}
func marshalCompletion() []byte {
	b := vint(nil, 1, 1)
	b = vint(b, 2, 64000)
	b = vint(b, 3, 200)
	b = dbl(b, 5, .4)
	b = dbl(b, 6, .4)
	b = vint(b, 7, 50)
	b = dbl(b, 8, 1)
	for _, s := range []string{"<|user|>", "<|bot|>", "<|context_request|>", "<|endoftext|>", "<|end_of_turn|>"} {
		b = str(b, 9, s)
	}
	return dbl(b, 11, 1)
}
func (r ChatRequest) Marshal() []byte {
	b := msg(nil, 1, r.Metadata.Marshal())
	b = str(b, 2, r.System)
	for _, p := range r.Prompts {
		b = msg(b, 3, marshalPrompt(p))
	}
	b = vint(b, 7, 5)
	b = msg(b, 8, marshalCompletion())
	for _, t := range r.Tools {
		b = msg(b, 10, marshalTool(t))
	}
	choice := str(nil, 1, r.ToolChoice)
	if r.ToolName != "" {
		choice = str(nil, 2, r.ToolName)
	}
	if len(r.Tools) > 0 {
		if len(choice) == 0 {
			choice = str(nil, 1, "auto")
		}
		b = msg(b, 12, choice)
	}
	b = msg(b, 13, vint(nil, 1, 1))
	b = str(b, 16, r.CascadeID)
	b = vint(b, 20, 1)
	b = str(b, 21, r.ModelUID)
	b = str(b, 22, r.ExecutionID)
	return str(b, 26, r.AssignmentJWT)
}

type ChatDelta struct {
	Text, Thinking, ActualModel string
	ToolCalls                   []ToolCall
	StopReason                  uint64
}

func DecodeChatDelta(b []byte) (ChatDelta, error) {
	f, e := fields(b)
	if e != nil {
		return ChatDelta{}, e
	}
	d := ChatDelta{Text: firstString(f[3]), Thinking: firstString(f[9]), ActualModel: firstString(f[23]), StopReason: firstVarint(f[5])}
	for _, raw := range f[6] {
		tf, err := fields(raw)
		if err != nil {
			return d, err
		}
		d.ToolCalls = append(d.ToolCalls, ToolCall{ID: firstString(tf[1]), Name: firstString(tf[2]), Arguments: firstString(tf[3])})
	}
	return d, nil
}

func MarshalAssign(meta Metadata, router, cascade string, prompt Prompt) []byte {
	b := msg(nil, 1, meta.Marshal())
	b = str(b, 2, router)
	b = str(b, 3, cascade)
	return msg(b, 5, marshalPrompt(prompt))
}
func DecodeAssignment(b []byte) (uid, jwt string, err error) {
	f, e := fields(b)
	if e != nil {
		return "", "", e
	}
	if len(f[1]) == 0 {
		return "", "", errors.New("AssignModel returned no assignment")
	}
	a, e := fields(f[1][0])
	if e != nil {
		return "", "", e
	}
	jwt = firstString(a[1])
	uid = firstString(a[2])
	if uid == "" || jwt == "" {
		err = errors.New("AssignModel returned incomplete assignment")
	}
	return
}

func fields(b []byte) (map[protowire.Number][][]byte, error) {
	out := map[protowire.Number][][]byte{}
	for len(b) > 0 {
		n, t, k := protowire.ConsumeTag(b)
		if k < 0 {
			return nil, protowire.ParseError(k)
		}
		b = b[k:]
		var raw []byte
		switch t {
		case protowire.BytesType:
			v, m := protowire.ConsumeBytes(b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			raw = append([]byte(nil), v...)
			b = b[m:]
		case protowire.VarintType:
			v, m := protowire.ConsumeVarint(b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			raw = make([]byte, 8)
			binary.LittleEndian.PutUint64(raw, v)
			b = b[m:]
		default:
			m := protowire.ConsumeFieldValue(n, t, b)
			if m < 0 {
				return nil, protowire.ParseError(m)
			}
			b = b[m:]
			continue
		}
		out[n] = append(out[n], raw)
	}
	return out, nil
}
func firstString(v [][]byte) string {
	if len(v) == 0 {
		return ""
	}
	return string(v[0])
}
func firstVarint(v [][]byte) uint64 {
	if len(v) == 0 || len(v[0]) != 8 {
		return 0
	}
	return binary.LittleEndian.Uint64(v[0])
}
