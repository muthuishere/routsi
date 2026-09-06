package backend

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/muthuishere/routsi/internal/api"
	"github.com/muthuishere/routsi/internal/backend/devinwire"
	"github.com/muthuishere/routsi/internal/config"
)

const (
	devinOrigin     = "https://server.codeium.com"
	devinAuthPath   = "/exa.auth_pb.AuthService/GetUserJwt"
	devinModelsPath = "/exa.api_server_pb.ApiServerService/GetCliModelConfigs"
	devinAssignPath = "/exa.api_server_pb.ApiServerService/AssignModel"
	devinChatPath   = "/exa.api_server_pb.ApiServerService/GetChatMessage"
	maxDevinBody    = 16 << 20
)

// DevinDirect uses Devin's authenticated Connect/protobuf service directly.
// The Devin executable is never launched; its local login file is merely one
// supported credential store.
type DevinDirect struct {
	model           *config.Model
	client          *http.Client
	credentialsPath string
}

func init() {
	RegisterSubscription("devin", func(_ context.Context, m *config.Model, client *http.Client) (Backend, error) {
		return NewDevinDirect(m, client), nil
	})
}

func NewDevinDirect(m *config.Model, client *http.Client) *DevinDirect {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Minute}
	}
	home, _ := os.UserHomeDir()
	return &DevinDirect{model: m, client: client, credentialsPath: filepath.Join(home, ".local", "share", "devin", "credentials.toml")}
}

func (d *DevinDirect) Complete(ctx context.Context, req *api.ChatRequest) (string, error) {
	r, e := d.CompleteResult(ctx, req)
	return r.Content, e
}
func (d *DevinDirect) Stream(ctx context.Context, req *api.ChatRequest, emit func(string)) error {
	r, e := d.CompleteResult(ctx, req)
	if e == nil && r.Content != "" {
		emit(r.Content)
	}
	return e
}

func (d *DevinDirect) CompleteResult(ctx context.Context, req *api.ChatRequest) (api.Result, error) {
	key, err := readDevinCredential(d.credentialsPath)
	if err != nil {
		return api.Result{}, err
	}
	jwt, base, err := d.authenticate(ctx, key)
	if err != nil {
		return api.Result{}, err
	}
	model, err := d.resolveModel(ctx, key)
	if err != nil {
		return api.Result{}, err
	}
	cascade := req.ConversationID
	if cascade == "" || strings.HasPrefix(cascade, "fp-") {
		cascade = uuid.NewString()
	}
	prompts, system, err := devinPrompts(req, cascade)
	if err != nil {
		return api.Result{}, err
	}
	tools, err := devinTools(req.Tools)
	if err != nil {
		return api.Result{}, err
	}
	assignmentJWT := ""
	if model.Router {
		uid, token, e := d.assign(ctx, key, model.UID, cascade, prompts)
		if e != nil {
			return api.Result{}, e
		}
		model.UID, assignmentJWT = uid, token
	}
	wreq := devinwire.ChatRequest{Metadata: devinwire.Metadata{APIKey: key, UserJWT: jwt}, System: system, ModelUID: model.UID, CascadeID: cascade, ExecutionID: uuid.NewString(), AssignmentJWT: assignmentJWT, Prompts: prompts, Tools: tools}
	wreq.ToolChoice, wreq.ToolName, err = devinToolChoice(req.ToolChoice)
	if err != nil {
		return api.Result{}, err
	}
	return d.chat(ctx, base, wreq)
}

func readDevinCredential(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", errors.New("Devin login unavailable")
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 || before.Mode().Perm()&0o077 != 0 {
		return "", errors.New("Devin credential file is not protected")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", errors.New("Devin login unavailable")
	}
	defer f.Close()
	after, err := f.Stat()
	if err != nil || !os.SameFile(before, after) {
		return "", errors.New("Devin credential file changed while opening")
	}
	b, err := io.ReadAll(io.LimitReader(f, 64<<10))
	if err != nil {
		return "", errors.New("Devin credential unreadable")
	}
	defer func() {
		for i := range b {
			b[i] = 0
		}
	}()
	for _, line := range strings.Split(string(b), "\n") {
		p := strings.SplitN(strings.TrimSpace(line), "=", 2)
		if len(p) == 2 && strings.TrimSpace(p[0]) == "windsurf_api_key" {
			v := strings.Trim(strings.TrimSpace(p[1]), `"`)
			if v != "" {
				return v, nil
			}
		}
	}
	return "", errors.New("Devin login credential missing")
}

func (d *DevinDirect) unary(ctx context.Context, path string, body []byte) ([]byte, error) {
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, devinOrigin+path, bytes.NewReader(body))
	if err != nil {
		return nil, errors.New("cannot create Devin request")
	}
	r.Header.Set("Content-Type", "application/proto")
	r.Header.Set("Connect-Protocol-Version", "1")
	r.Header.Set("Accept", "*/*")
	resp, err := d.client.Do(r)
	if err != nil {
		return nil, errors.New("Devin transport failed")
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxDevinBody+1))
	if err != nil || len(b) > maxDevinBody {
		return nil, errors.New("Devin response unreadable")
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("Devin RPC returned HTTP %d", resp.StatusCode)
	}
	if len(b) >= 2 && b[0] == 0x1f && b[1] == 0x8b {
		gz, e := gzip.NewReader(bytes.NewReader(b))
		if e != nil {
			return nil, errors.New("invalid Devin gzip response")
		}
		defer gz.Close()
		return io.ReadAll(io.LimitReader(gz, maxDevinBody+1))
	}
	return b, nil
}

func (d *DevinDirect) authenticate(ctx context.Context, key string) (string, string, error) {
	b, err := d.unary(ctx, devinAuthPath, devinwire.MarshalAuthRequest(devinwire.Metadata{APIKey: key}))
	if err != nil {
		return "", "", err
	}
	jwt, custom, err := devinwire.DecodeAuthResponse(b)
	if err != nil {
		return "", "", err
	}
	base := devinOrigin
	if custom != "" {
		u, e := url.Parse(custom)
		if e != nil || u.Scheme != "https" || u.Hostname() != "server.codeium.com" || u.Port() != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Path != "" && u.Path != "/") {
			return "", "", errors.New("Devin returned an untrusted API origin")
		}
		base = strings.TrimRight(custom, "/")
	}
	return jwt, base, nil
}

func (d *DevinDirect) resolveModel(ctx context.Context, key string) (devinwire.Model, error) {
	b, err := d.unary(ctx, devinModelsPath, devinwire.MarshalDiscoveryRequest(devinwire.Metadata{APIKey: key}))
	if err != nil {
		return devinwire.Model{}, err
	}
	models, def, err := devinwire.DecodeModels(b)
	if err != nil {
		return devinwire.Model{}, errors.New("invalid Devin model response")
	}
	want := d.model.UpstreamModel
	for _, m := range models {
		if !m.Disabled && want != "" && (m.UID == want || strings.EqualFold(m.Label, want)) {
			return m, nil
		}
	}
	if want != "" {
		return devinwire.Model{}, fmt.Errorf("Devin model %q is unavailable", want)
	}
	for _, m := range models {
		if !m.Disabled && m.UID == def {
			return m, nil
		}
	}
	for _, m := range models {
		if !m.Disabled {
			return m, nil
		}
	}
	return devinwire.Model{}, errors.New("Devin account has no available model")
}

func (d *DevinDirect) assign(ctx context.Context, key, router, cascade string, prompts []devinwire.Prompt) (string, string, error) {
	var latest devinwire.Prompt
	for i := len(prompts) - 1; i >= 0; i-- {
		if prompts[i].Source == 1 {
			latest = prompts[i]
			break
		}
	}
	b, err := d.unary(ctx, devinAssignPath, devinwire.MarshalAssign(devinwire.Metadata{APIKey: key}, router, cascade, latest))
	if err != nil {
		return "", "", err
	}
	return devinwire.DecodeAssignment(b)
}

func (d *DevinDirect) chat(ctx context.Context, base string, request devinwire.ChatRequest) (api.Result, error) {
	raw := request.Marshal()
	var zipped bytes.Buffer
	gz := gzip.NewWriter(&zipped)
	if _, err := gz.Write(raw); err != nil {
		return api.Result{}, err
	}
	if err := gz.Close(); err != nil {
		return api.Result{}, err
	}
	frame := make([]byte, 5+zipped.Len())
	frame[0] = 1
	binary.BigEndian.PutUint32(frame[1:5], uint32(zipped.Len()))
	copy(frame[5:], zipped.Bytes())
	r, err := http.NewRequestWithContext(ctx, http.MethodPost, base+devinChatPath, bytes.NewReader(frame))
	if err != nil {
		return api.Result{}, err
	}
	r.Header.Set("Content-Type", "application/connect+proto")
	r.Header.Set("Connect-Protocol-Version", "1")
	r.Header.Set("Connect-Content-Encoding", "gzip")
	r.Header.Set("Connect-Accept-Encoding", "gzip")
	r.Header.Set("Accept-Encoding", "identity")
	resp, err := d.client.Do(r)
	if err != nil {
		return api.Result{}, errors.New("Devin chat transport failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return api.Result{}, fmt.Errorf("Devin chat returned HTTP %d", resp.StatusCode)
	}
	reader := bufio.NewReader(resp.Body)
	result := api.Result{}
	calls := map[string]*api.ToolCall{}
	var order []string
	for {
		header := make([]byte, 5)
		_, err := io.ReadFull(reader, header)
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return api.Result{}, errors.New("incomplete Devin stream")
		}
		n := binary.BigEndian.Uint32(header[1:])
		if n > maxDevinBody {
			return api.Result{}, errors.New("Devin stream frame too large")
		}
		payload := make([]byte, n)
		if _, err := io.ReadFull(reader, payload); err != nil {
			return api.Result{}, errors.New("incomplete Devin frame")
		}
		if header[0]&1 != 0 {
			z, e := gzip.NewReader(bytes.NewReader(payload))
			if e != nil {
				return api.Result{}, errors.New("invalid compressed Devin frame")
			}
			payload, e = io.ReadAll(io.LimitReader(z, maxDevinBody+1))
			z.Close()
			if e != nil {
				return api.Result{}, errors.New("invalid compressed Devin frame")
			}
		}
		if header[0]&2 != 0 {
			var trailer struct {
				Error *struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			_ = json.Unmarshal(payload, &trailer)
			if trailer.Error != nil {
				return api.Result{}, fmt.Errorf("Devin stream error %s", trailer.Error.Code)
			}
			continue
		}
		delta, e := devinwire.DecodeChatDelta(payload)
		if e != nil {
			return api.Result{}, errors.New("invalid Devin chat protobuf")
		}
		result.Content += delta.Text
		for _, tc := range delta.ToolCalls {
			id := tc.ID
			if id == "" && len(order) > 0 {
				id = order[len(order)-1]
			}
			if id == "" {
				continue
			}
			call := calls[id]
			if call == nil {
				call = &api.ToolCall{ID: id, Type: "function"}
				calls[id] = call
				order = append(order, id)
			}
			if tc.Name != "" {
				call.Function.Name = tc.Name
			}
			if tc.Arguments != "" {
				if strings.HasPrefix(tc.Arguments, call.Function.Arguments) {
					call.Function.Arguments = tc.Arguments
				} else {
					call.Function.Arguments += tc.Arguments
				}
			}
		}
	}
	for _, id := range order {
		call := *calls[id]
		if call.Function.Arguments == "" {
			call.Function.Arguments = "{}"
		}
		result.ToolCalls = append(result.ToolCalls, call)
	}
	return result, nil
}

func devinPrompts(req *api.ChatRequest, cascade string) ([]devinwire.Prompt, string, error) {
	var out []devinwire.Prompt
	var systems []string
	for i, m := range req.Messages {
		switch m.Role {
		case "system", "developer":
			systems = append(systems, m.Text())
		case "user":
			out = append(out, devinwire.Prompt{ID: uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s:%d:user", cascade, i))).String(), Source: 1, Text: m.Text()})
		case "assistant":
			p := devinwire.Prompt{ID: "bot-" + uuid.NewSHA1(uuid.NameSpaceOID, []byte(fmt.Sprintf("%s:%d:assistant", cascade, i))).String(), Source: 2, Text: m.Text()}
			if len(m.ToolCalls) > 0 {
				var calls []api.ToolCall
				if err := json.Unmarshal(m.ToolCalls, &calls); err != nil {
					return nil, "", errors.New("invalid assistant tool calls")
				}
				for _, c := range calls {
					p.ToolCalls = append(p.ToolCalls, devinwire.ToolCall{ID: c.ID, Name: c.Function.Name, Arguments: c.Function.Arguments})
				}
			}
			out = append(out, p)
		case "tool":
			out = append(out, devinwire.Prompt{ID: uuid.NewString(), Source: 4, Text: m.Text(), ToolCallID: m.ToolCallID})
		}
	}
	return out, strings.Join(systems, "\n\n"), nil
}

func devinTools(raw json.RawMessage) ([]devinwire.Tool, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var in []struct {
		Function struct {
			Name, Description string
			Parameters        json.RawMessage
			Strict            bool
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &in); err != nil {
		return nil, errors.New("invalid tools")
	}
	out := make([]devinwire.Tool, 0, len(in))
	for _, t := range in {
		schema := string(t.Function.Parameters)
		if schema == "" {
			schema = "{}"
		}
		out = append(out, devinwire.Tool{Name: t.Function.Name, Description: t.Function.Description, JSONSchema: schema, Strict: t.Function.Strict})
	}
	return out, nil
}

func devinToolChoice(raw json.RawMessage) (option, name string, err error) {
	if len(raw) == 0 || string(raw) == "null" {
		return "auto", "", nil
	}
	if json.Unmarshal(raw, &option) == nil {
		switch option {
		case "auto", "none", "required":
			return option, "", nil
		default:
			return "", "", errors.New("unsupported tool_choice")
		}
	}
	var selected struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &selected) != nil || selected.Type != "function" || selected.Function.Name == "" {
		return "", "", errors.New("invalid tool_choice")
	}
	return "", selected.Function.Name, nil
}
