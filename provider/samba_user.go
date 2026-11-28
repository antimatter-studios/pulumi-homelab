package homelab

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"slices"
	"strings"

	"github.com/antimatter-studios/pulumi-homelab/provider/samba"
	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"
)

// SambaUser is a samba password for an existing unix account. Ported from src/resources/samba.ts
// SambaUser.
type SambaUser struct{}

// SambaUserArgs are the inputs of a SambaUser.
type SambaUserArgs struct {
	Name     string `pulumi:"name"`
	Password string `pulumi:"password" provider:"secret"`
}

// SambaUserState is what is kept in state.
type SambaUserState struct {
	Name string `pulumi:"name"`
	// Password is nil after an import: samba cannot give it back, so an imported user does not
	// know it, and Diff compares it only once a Create or Update has recorded it.
	Password *string `pulumi:"password,optional" provider:"secret"`
	Exists   bool    `pulumi:"exists"`
	Created  *bool   `pulumi:"created,optional"`
}

func (s *SambaUserArgs) Annotate(a infer.Annotator) {
	a.Describe(&s.Name, "The unix account samba maps the user onto. It must already exist.")
	a.Describe(&s.Password, "The samba password.")
}

func (s *SambaUserState) Annotate(a infer.Annotator) {
	a.Describe(&s.Created, "Whether the provider added this Samba account, used to guard deletion.")
}

func (s *SambaUser) Annotate(a infer.Annotator) {
	a.Describe(&s, "A samba user: a password in samba's database for an existing unix account.")
	a.SetToken("index", "SambaUser")
}

const listSambaUsers = "pdbedit -L"

func readSambaUser(ctx context.Context, m *machine, name string) (bool, error) {
	users, err := m.must(ctx, m.escalate(listSambaUsers))
	if err != nil {
		return false, err
	}
	return slices.ContainsFunc(samba.ParseSambaUsers(users), func(user string) bool {
		return strings.EqualFold(user, name)
	}), nil
}

// setSambaPassword sends both password lines on stdin. When sudo may read its own
// password first, a marker lets the shell skip that line if sudo did not prompt.
func setSambaPassword(ctx context.Context, m *machine, name, password string) error {
	command := "smbpasswd -s -a " + shellQuote(name) + " >/dev/null"
	stdin := password + "\n" + password + "\n"
	if m.cfg.Escalation == "sudo-password" {
		var nonce [16]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		marker := "pulumi-homelab-" + hex.EncodeToString(nonce[:])
		command = "while IFS= read -r line; do [ \"$line\" = " + shellQuote(marker) +
			" ] && break; done; " + command
		stdin = m.cfg.SudoPassword + "\n" + marker + "\n" + stdin
	}
	_, _, code, err := m.runner.Run(ctx, m.escalate(command), []byte(stdin))
	if err != nil {
		return fmt.Errorf("smbpasswd failed for user %s on %s: the connection failed", name, m.describe())
	}
	if code != 0 {
		// smbpasswd's own output never reaches the error: it can echo the password in forms no
		// redaction catches (trimmed, CRLF, one line of a multiline password).
		return fmt.Errorf("smbpasswd failed for user %s on %s (exit %d)", name, m.describe(), code)
	}
	return nil
}

// Create sets the password of an existing unix account.
func (SambaUser) Create(ctx context.Context, req infer.CreateRequest[SambaUserArgs]) (infer.CreateResponse[SambaUserState], error) {
	password := req.Inputs.Password
	state := SambaUserState{Name: req.Inputs.Name, Password: &password, Exists: true}
	if req.DryRun {
		return infer.CreateResponse[SambaUserState]{ID: req.Inputs.Name, Output: state}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[SambaUserState]{}, err
	}
	unix, err := m.ask(ctx, "getent passwd "+shellQuote(req.Inputs.Name))
	if err != nil {
		return infer.CreateResponse[SambaUserState]{}, err
	}
	if unix.code != 0 {
		return infer.CreateResponse[SambaUserState]{}, fmt.Errorf("there is no unix account called %s; samba maps onto one, so declare a User first", req.Inputs.Name)
	}
	exists, err := readSambaUser(ctx, m, req.Inputs.Name)
	if err != nil {
		return infer.CreateResponse[SambaUserState]{}, err
	}
	created := !exists
	state.Created = &created
	if err := setSambaPassword(ctx, m, req.Inputs.Name, req.Inputs.Password); err != nil {
		return infer.CreateResponse[SambaUserState]{}, err
	}
	return infer.CreateResponse[SambaUserState]{ID: req.Inputs.Name, Output: state}, nil
}

// Read reports whether samba knows the user; one gone from samba's database is forgotten. A
// password cannot be read back, so an import leaves it unknown.
func (SambaUser) Read(ctx context.Context, req infer.ReadRequest[SambaUserArgs, SambaUserState]) (infer.ReadResponse[SambaUserArgs, SambaUserState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[SambaUserArgs, SambaUserState]{}, err
	}
	exists, err := readSambaUser(ctx, m, req.ID)
	if err != nil {
		return infer.ReadResponse[SambaUserArgs, SambaUserState]{}, err
	}
	if !exists {
		return infer.ReadResponse[SambaUserArgs, SambaUserState]{}, nil
	}
	state := req.State
	state.Name, state.Exists = req.ID, true
	inputs := req.Inputs
	inputs.Name = req.ID
	return infer.ReadResponse[SambaUserArgs, SambaUserState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update sets the password again.
func (SambaUser) Update(ctx context.Context, req infer.UpdateRequest[SambaUserArgs, SambaUserState]) (infer.UpdateResponse[SambaUserState], error) {
	password := req.Inputs.Password
	state := SambaUserState{Name: req.ID, Password: &password, Exists: true, Created: req.State.Created}
	if req.DryRun {
		return infer.UpdateResponse[SambaUserState]{Output: state}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[SambaUserState]{}, err
	}
	if err := setSambaPassword(ctx, m, req.ID, req.Inputs.Password); err != nil {
		return infer.UpdateResponse[SambaUserState]{}, err
	}
	return infer.UpdateResponse[SambaUserState]{Output: state}, nil
}

// Diff: a new password is an update; a new name replaces, deleting first.
func (SambaUser) Diff(_ context.Context, req infer.DiffRequest[SambaUserArgs, SambaUserState]) (p.DiffResponse, error) {
	resp := p.DiffResponse{DeleteBeforeReplace: true, DetailedDiff: map[string]p.PropertyDiff{}}
	if req.State.Password != nil && *req.State.Password != req.Inputs.Password {
		resp.DetailedDiff["password"] = p.PropertyDiff{Kind: p.Update}
	}
	if req.State.Name != req.Inputs.Name {
		resp.DetailedDiff["name"] = p.PropertyDiff{Kind: p.UpdateReplace}
	}
	resp.HasChanges = len(resp.DetailedDiff) > 0
	return resp, nil
}

// Delete removes the user from samba's database, never the unix account or its files.
func (SambaUser) Delete(ctx context.Context, req infer.DeleteRequest[SambaUserState]) (infer.DeleteResponse, error) {
	if !deref(req.State.Created) {
		warnRetained(ctx, fmt.Sprintf("Samba account %q", req.ID), "state does not record that the provider created it.")
		return infer.DeleteResponse{}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	// A failed passdb lookup cannot tell us the user is already gone.
	exists, err := readSambaUser(ctx, m, req.ID)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	if !exists {
		return infer.DeleteResponse{}, nil
	}
	_, err = m.must(ctx, m.escalate("smbpasswd -x "+shellQuote(req.ID)))
	return infer.DeleteResponse{}, err
}
