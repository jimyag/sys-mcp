package apiproxy_test

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/jimyag/sysplane/internal/sysplane-agent/apiproxy"
)

func startLocalServer(t *testing.T) int {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := lis.Addr().(*net.TCPAddr).Port
	mux := http.NewServeMux()
	mux.HandleFunc("/hello", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"message":"hello"}`))
	})
	srv := &http.Server{Handler: mux}
	go srv.Serve(lis)
	t.Cleanup(func() { srv.Close() })
	return port
}

func TestProxy_SuccessfulCall(t *testing.T) {
	port := startLocalServer(t)
	p := apiproxy.New(apiproxy.Config{})
	argsJSON, _ := json.Marshal(apiproxy.CallParams{Port: port, Path: "/hello"})

	out, err := p.Call(context.Background(), string(argsJSON))
	if err != nil {
		t.Fatal(err)
	}
	var res apiproxy.CallResult
	json.Unmarshal([]byte(out), &res)
	if res.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", res.StatusCode)
	}
	if !res.BodyIsJSON {
		t.Fatal("expected body_is_json=true")
	}
}

func TestProxy_PrivilegedPortDenied(t *testing.T) {
	p := apiproxy.New(apiproxy.Config{AllowPrivilegedPorts: false})
	argsJSON, _ := json.Marshal(apiproxy.CallParams{Port: 80, Path: "/"})
	_, err := p.Call(context.Background(), string(argsJSON))
	if err == nil {
		t.Fatal("expected error for privileged port")
	}
}

func TestProxy_AllowedPortsFilter(t *testing.T) {
	port := startLocalServer(t)
	p := apiproxy.New(apiproxy.Config{AllowedPorts: []int{9999}}) // port not in list
	argsJSON, _ := json.Marshal(apiproxy.CallParams{Port: port, Path: "/"})
	_, err := p.Call(context.Background(), string(argsJSON))
	if err == nil {
		t.Fatalf("expected error: port %d not in allowed list", port)
	}
}
