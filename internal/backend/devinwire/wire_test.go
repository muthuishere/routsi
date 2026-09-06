package devinwire

import (
	"strings"
	"testing"
)

func TestMetadataCarriesPrefixedCredentialInsideProtobuf(t *testing.T) {
	const secret = "SECRET_CANARY"
	b := Metadata{APIKey: secret, UserJWT: "jwt-canary"}.Marshal()
	f, err := fields(b)
	if err != nil {
		t.Fatal(err)
	}
	if got := firstString(f[3]); got != "devin-session-token$"+secret {
		t.Fatalf("unexpected api_key")
	}
	if got := firstString(f[21]); got != "jwt-canary" {
		t.Fatalf("unexpected user_jwt")
	}
	if strings.Contains(string(b), "Bearer ") {
		t.Fatal("HTTP bearer scheme leaked into protobuf metadata")
	}
}

func TestDecodeModelsUsesUIDDisabledAndRouterFields(t *testing.T) {
	info := boolean(nil, 25, true)
	model := str(nil, 1, "Adaptive")
	model = str(model, 22, "adaptive")
	model = msg(model, 23, info)
	response := msg(nil, 1, model)
	response = msg(response, 3, str(nil, 3, "adaptive"))
	models, defaultUID, err := DecodeModels(response)
	if err != nil {
		t.Fatal(err)
	}
	if len(models) != 1 || models[0].UID != "adaptive" || !models[0].Router || defaultUID != "adaptive" {
		t.Fatalf("decoded %#v default=%q", models, defaultUID)
	}
}

func TestChatRequestCarriesNativeToolDefinition(t *testing.T) {
	b := (ChatRequest{Metadata: Metadata{APIKey: "x", UserJWT: "y"}, ModelUID: "model", CascadeID: "cascade", ExecutionID: "exec", ToolChoice: "required", Tools: []Tool{{Name: "weather", Description: "Weather", JSONSchema: `{"type":"object"}`, Strict: true}}}).Marshal()
	f, err := fields(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(f[10]) != 1 {
		t.Fatal("native tool definition missing")
	}
	tool, err := fields(f[10][0])
	if err != nil {
		t.Fatal(err)
	}
	if firstString(tool[1]) != "weather" || firstString(tool[3]) != `{"type":"object"}` || firstVarint(tool[12]) != 1 {
		t.Fatal("native tool fields differ")
	}
	if len(f[12]) != 1 {
		t.Fatal("native tool choice missing")
	}
	choice, _ := fields(f[12][0])
	if firstString(choice[1]) != "required" {
		t.Fatal("tool choice is not required")
	}
}
