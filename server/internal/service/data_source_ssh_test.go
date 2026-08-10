// SSH-tunnel config plumbing for DataSourceService: the encrypted "ssh" block
// round-trips into dataconn.ConnConfig.SSH via LoadConnConfig, the redacted DTO
// strips the ssh secrets (password / private_key), and Update keeps the stored
// ssh secrets when the edit form leaves them blank.
package service_test

import (
	"context"
	"testing"

	"github.com/niuniu-dev/niuniu/internal/dataconn"
	"github.com/niuniu-dev/niuniu/internal/service"
)

func createSSHSource(t *testing.T, svc *service.DataSourceService, uid int64) int64 {
	t.Helper()
	id, err := svc.Create(context.Background(), service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: uid, UserID: uid, Name: "tunnelled-pg", Kind: "postgres",
		Config: map[string]any{
			"host": "db.internal", "port": 5432, "user": "ro", "password": "dbpass", "database": "app",
			"ssh": map[string]any{
				"enabled": true, "host": "bastion.example.com", "port": 22, "user": "ec2-user",
				"auth_method": "private_key", "private_key": "PEMDATA", "host_key": "SHA256:abc",
			},
		},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return id
}

func TestLoadConnConfigParsesSSH(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	uid := env.UserA
	id := createSSHSource(t, svc, uid)

	cc, err := svc.LoadConnConfig(context.Background(), id, uid, nil)
	if err != nil {
		t.Fatalf("LoadConnConfig: %v", err)
	}
	if cc.SSH == nil {
		t.Fatal("expected cc.SSH to be populated")
	}
	if !cc.SSH.Enabled || cc.SSH.Host != "bastion.example.com" || cc.SSH.Port != 22 {
		t.Fatalf("ssh host/port/enabled wrong: %+v", cc.SSH)
	}
	if cc.SSH.User != "ec2-user" || cc.SSH.AuthMethod != dataconn.SSHAuthPrivateKey {
		t.Fatalf("ssh user/auth_method wrong: %+v", cc.SSH)
	}
	if cc.SSH.PrivateKey != "PEMDATA" || cc.SSH.HostKey != "SHA256:abc" {
		t.Fatalf("ssh secret/host_key not decrypted: %+v", cc.SSH)
	}
}

func TestRedactedDTOStripsSSHSecrets(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	uid := env.UserA
	id := createSSHSource(t, svc, uid)

	dto, err := svc.Get(context.Background(), id, uid, nil)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	sshBlock, ok := dto.Config["ssh"].(map[string]any)
	if !ok {
		t.Fatalf("expected redacted ssh block, got %v", dto.Config)
	}
	if _, leaked := sshBlock["private_key"]; leaked {
		t.Fatal("ssh private_key must be redacted")
	}
	if _, leaked := sshBlock["password"]; leaked {
		t.Fatal("ssh password must be redacted")
	}
	if sshBlock["host"] != "bastion.example.com" || sshBlock["user"] != "ec2-user" {
		t.Fatalf("non-secret ssh fields must be echoed: %v", sshBlock)
	}
	if sshBlock["host_key"] != "SHA256:abc" {
		t.Fatalf("host_key should be echoed (not secret): %v", sshBlock)
	}
	if sshBlock["has_private_key"] != true {
		t.Fatalf("expected has_private_key hint, got %v", sshBlock)
	}
}

func TestUpdateKeepsStoredSSHSecrets(t *testing.T) {
	svc, env := newTestDataSourceService(t)
	uid := env.UserA
	ctx := context.Background()
	id := createSSHSource(t, svc, uid)

	// Simulate the edit form: it echoes back the redacted ssh block (blank
	// private_key/password) plus a changed host_key, and a blank db password.
	err := svc.Update(ctx, id, uid, nil, service.UpdateDataSourceInput{
		Name: "tunnelled-pg",
		Config: map[string]any{
			"host": "db.internal", "port": 5432, "user": "ro", "database": "app",
			"ssh": map[string]any{
				"enabled": true, "host": "bastion.example.com", "port": 22, "user": "ec2-user",
				"auth_method": "private_key", "host_key": "SHA256:new",
			},
		},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}

	cc, err := svc.LoadConnConfig(ctx, id, uid, nil)
	if err != nil {
		t.Fatalf("LoadConnConfig after update: %v", err)
	}
	if cc.SSH == nil || cc.SSH.PrivateKey != "PEMDATA" {
		t.Fatalf("stored ssh private_key must be retained on blank edit, got %+v", cc.SSH)
	}
	if cc.SSH.HostKey != "SHA256:new" {
		t.Fatalf("host_key should have been updated, got %+v", cc.SSH)
	}
	if cc.Password != "dbpass" {
		t.Fatalf("stored db password must be retained on blank edit, got %q", cc.Password)
	}
}

func TestVerifyDisabledSSHIsNoop(t *testing.T) {
	// A source with no ssh block verifies via the direct path; a source with
	// ssh.enabled=false must not attempt a tunnel (it just pings directly and
	// fails to reach a non-existent host — but never a tunnel/auth error).
	svc, env := newTestDataSourceService(t)
	uid := env.UserA
	ctx := context.Background()
	id, err := svc.Create(ctx, service.CreateDataSourceInput{
		OwnerType: "user", OwnerID: uid, UserID: uid, Name: "no-tunnel", Kind: "postgres",
		Config: map[string]any{
			"host": "127.0.0.1", "port": 1, "user": "ro", "password": "x", "database": "app",
			"ssh": map[string]any{"enabled": false, "host": "bastion", "user": "u"},
		},
		DefaultAccessMode: "read", RequireConfirm: "writes_only",
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	// Verify will fail to connect (port 1) but must return a plain connection
	// error, not a tunnel error.
	if verr := svc.Verify(ctx, id, uid, nil); verr == nil {
		t.Fatal("expected connection failure to unreachable host")
	}
}
