package main

import (
	"bytes"
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/SigmaUno/subspace/internal/doctor"
	"github.com/SigmaUno/subspace/internal/store/sqlite"
)

func doctorEnv(vars map[string]string) doctor.Env {
	e := doctor.SystemEnv()
	e.Getenv = func(k string) string { return vars[k] }
	return e
}

func TestDoctorCommand(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "subspace.db")
	st, err := sqlite.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Backup(context.Background(), filepath.Join(dir, "backups", "subspace-"+time.Now().UTC().Format("20060102T150405Z")+".db")); err != nil {
		t.Fatal(err)
	}
	if err := st.Close(); err != nil {
		t.Fatal(err)
	}
	vars := map[string]string{
		"SUBSPACE_DB": path, "SUBSPACE_ADDR": "127.0.0.1:8080", "SUBSPACE_OWNER_TOKEN": "Zk3qT9vLx2WmB7nRc4HdYe8PaUf1GjS6",
	}

	var out bytes.Buffer
	err = runDoctorEnv([]string{"--json"}, &out, doctorEnv(vars))
	var rep doctor.Report
	if jerr := json.Unmarshal(out.Bytes(), &rep); jerr != nil {
		t.Fatalf("json output: %v\n%s", jerr, out.String())
	}
	if rep.Errors != 0 || err != nil {
		t.Fatalf("healthy instance: err=%v\n%s", err, out.String())
	}

	vars["SUBSPACE_OWNER_TOKEN"] = ""
	out.Reset()
	err = runDoctorEnv(nil, &out, doctorEnv(vars))
	if err == nil || !strings.Contains(err.Error(), "1 errors") || !strings.Contains(out.String(), "[error] config.owner_token") {
		t.Fatalf("err=%v\n%s", err, out.String())
	}
}
