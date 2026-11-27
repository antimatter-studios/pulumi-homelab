package homelab

import (
	"context"
	"errors"
	"fmt"
	"strings"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/mode"
	"github.com/antimatter-studios/pulumi-homelab/provider/sshkey"
)

// SshKey is a key pair generated on the machine, whose public half is an output. Ported from
// src/resources/sshkey.ts. The private half never leaves the machine, and delete refuses unless
// allowDelete is set: the key may be authorised somewhere this stack does not know about.
type SshKey struct{}

// SshKeyArgs are the inputs of an SshKey.
type SshKeyArgs struct {
	Path        string  `pulumi:"path"`
	Type        *string `pulumi:"type,optional"`
	Bits        *int    `pulumi:"bits,optional"`
	Comment     *string `pulumi:"comment,optional"`
	Owner       *string `pulumi:"owner,optional"`
	Group       *string `pulumi:"group,optional"`
	AllowDelete *bool   `pulumi:"allowDelete,optional"`
}

// SshKeyState is what is kept in state. The private key is not in it.
type SshKeyState struct {
	Path        string `pulumi:"path"`
	Type        string `pulumi:"type"`
	Comment     string `pulumi:"comment"`
	Owner       string `pulumi:"owner"`
	Group       string `pulumi:"group"`
	PrivateMode string `pulumi:"privateMode"`
	PublicMode  string `pulumi:"publicMode"`
	AllowDelete bool   `pulumi:"allowDelete"`
	PublicKey   string `pulumi:"publicKey"`
	Fingerprint string `pulumi:"fingerprint"`
}

func (a *SshKeyArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.Path, "Where the private key goes; the public key is the same path with .pub.")
	an.Describe(&a.Type, "ed25519 (the default), rsa or ecdsa.")
	an.Describe(&a.Bits, "Bits, for the types that take them. Ignored by ed25519.")
	an.Describe(&a.Comment, "What goes in the key's comment field. Defaults to the resource's name.")
	an.Describe(&a.Owner, "Who owns both halves of the pair. Defaults to root.")
	an.Describe(&a.Group, "The group on both halves. Defaults to root.")
	an.Describe(&a.AllowDelete, "Permission to delete the private key when the resource goes. Off, and worth leaving off.")
}

func (s *SshKeyState) Annotate(an infer.Annotator) {
	an.Describe(&s.PublicKey, "The public key line, for handing to whatever should trust it.")
	an.Describe(&s.Fingerprint, "SHA256:…, the whole identity of a key pair, safe to print anywhere.")
}

func (r *SshKey) Annotate(a infer.Annotator) {
	a.Describe(&r, "A key pair the machine should have, whose public half can be handed to whatever should trust it.")
	a.SetToken("index", "SshKey")
}

const (
	privateKeyMode = "0600"
	publicKeyMode  = "0644"
)

// keyTypeOf is the SshKey type a public key line is: ssh-ed25519, ssh-rsa or ecdsa-sha2-*.
func keyTypeOf(publicKey string) string {
	words := jscompat.Fields(publicKey)
	if len(words) == 0 {
		return ""
	}
	switch {
	case words[0] == "ssh-ed25519":
		return "ed25519"
	case words[0] == "ssh-rsa":
		return "rsa"
	case strings.HasPrefix(words[0], "ecdsa-sha2-"):
		return "ecdsa"
	}
	return ""
}

// keyCommentOf is the comment of a public key line, everything after the base64 body.
func keyCommentOf(publicKey string) string {
	words := jscompat.Fields(publicKey)
	if len(words) < 3 {
		return ""
	}
	text := jscompat.Trim(publicKey)
	return jscompat.Trim(text[strings.Index(text, words[1])+len(words[1]):])
}

// keyFiles is what a key pair's files are, beyond the key itself.
type keyFiles struct {
	PrivateMode, PublicMode, Owner, Group string
}

// parseKeyFiles reads `600|svc|svc` for each half, private first.
func parseKeyFiles(out string) keyFiles {
	lines := append(strings.Split(jscompat.Trim(out), "\n"), "", "")
	first := append(strings.Split(jscompat.Trim(lines[0]), "|"), "", "", "")
	second := strings.Split(jscompat.Trim(lines[1]), "|")
	files := keyFiles{Owner: first[1], Group: first[2]}
	if first[0] != "" {
		files.PrivateMode = mode.NormaliseMode(first[0])
	}
	if second[0] != "" {
		files.PublicMode = mode.NormaliseMode(second[0])
	}
	return files
}

// permissionsWrong says whether the files need correcting; empty fields are skipped.
func permissionsWrong(actual keyFiles, owner, group string) bool {
	if actual.PrivateMode != "" && actual.PrivateMode != privateKeyMode {
		return true
	}
	if actual.PublicMode != "" && actual.PublicMode != publicKeyMode {
		return true
	}
	if actual.Owner != "" && actual.Owner != owner {
		return true
	}
	return actual.Group != "" && actual.Group != group
}

// permissionsCommand narrows the private half before ownership moves.
func permissionsCommand(path, owner, group string) string {
	private, public := shellQuote(path), shellQuote(path+".pub")
	return "chmod " + privateKeyMode + " " + private + " && chmod " + publicKeyMode + " " + public + " && " +
		"chown " + shellQuote(owner+":"+group) + " " + private + " " + public
}

const (
	fingerprintMarker = "#pulumi-homelab#fingerprint"
	statMarker        = "#pulumi-homelab#stat"
)

type keyOnDisk struct {
	keyFiles
	PublicKey, Fingerprint string
}

func sshKeyReadCommand(path string) string {
	return "test -f " + shellQuote(path) + " || exit 9; " +
		"cat " + shellQuote(path+".pub") + "; echo '" + fingerprintMarker + "'; " +
		"ssh-keygen -lf " + shellQuote(path+".pub") + " 2>/dev/null || true; echo '" + statMarker + "'; " +
		"stat -c '%a|%U|%G' " + shellQuote(path) + " " + shellQuote(path+".pub") + " 2>/dev/null || true"
}

func parseKeyOnDisk(out string) keyOnDisk {
	publicKey, rest := splitOnce(out, fingerprintMarker+"\n")
	printed, stat := splitOnce(rest, statMarker+"\n")
	return keyOnDisk{keyFiles: parseKeyFiles(stat), PublicKey: jscompat.Trim(publicKey), Fingerprint: sshkey.ParseFingerprint(printed)}
}

func readSshKey(ctx context.Context, m *machine, path string) (*keyOnDisk, error) {
	asked, err := m.ask(ctx, m.escalate(sshKeyReadCommand(path)))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read the key at %s: %s", path, jscompat.Trim(asked.err))
	}
	read := parseKeyOnDisk(asked.out)
	return &read, nil
}

// sshKeyGenerateCommand makes the pair, with no passphrase, in a 0700 directory.
func sshKeyGenerateCommand(path, keyType string, bits *int, comment string) string {
	b := ""
	if bits != nil && keyType != "ed25519" {
		b = fmt.Sprintf("-b %d ", *bits)
	}
	directory := parentOf(path)
	return "mkdir -p " + shellQuote(directory) + " && chmod 0700 " + shellQuote(directory) + " && " +
		"ssh-keygen -t " + shellQuote(keyType) + " " + b + "-N '' -C " + shellQuote(comment) + " -f " + shellQuote(path) + " >/dev/null"
}

func orDefault(v *string, d string) string {
	if v != nil {
		return *v
	}
	return d
}

func settleSshKey(ctx context.Context, m *machine, a SshKeyArgs, name string) (SshKeyState, error) {
	keyType := orDefault(a.Type, "ed25519")
	comment := orDefault(a.Comment, name)
	owner, group := orDefault(a.Owner, "root"), orDefault(a.Group, "root")
	existing, err := readSshKey(ctx, m, a.Path)
	if err != nil {
		return SshKeyState{}, err
	}
	if existing == nil {
		if _, err := m.must(ctx, m.escalate(sshKeyGenerateCommand(a.Path, keyType, a.Bits, comment))); err != nil {
			return SshKeyState{}, err
		}
	}
	generated, err := readSshKey(ctx, m, a.Path)
	if err != nil {
		return SshKeyState{}, err
	}
	if generated == nil {
		return SshKeyState{}, fmt.Errorf("generated a key at %s but it is not there", a.Path)
	}
	if existing == nil || permissionsWrong(generated.keyFiles, owner, group) {
		if _, err := m.must(ctx, m.escalate(permissionsCommand(a.Path, owner, group))); err != nil {
			return SshKeyState{}, err
		}
	}
	actual, err := readSshKey(ctx, m, a.Path)
	if err != nil {
		return SshKeyState{}, err
	}
	if actual == nil {
		return SshKeyState{}, fmt.Errorf("generated a key at %s but it is not there", a.Path)
	}
	if permissionsWrong(actual.keyFiles, owner, group) {
		return SshKeyState{}, fmt.Errorf("the key at %s on %s is %s %s:%s rather than %s %s:%s — check that both the owner and the group exist",
			a.Path, m.describe(), actual.PrivateMode, actual.Owner, actual.Group, privateKeyMode, owner, group)
	}
	return SshKeyState{
		Path: a.Path, Type: keyType, Comment: comment, Owner: actual.Owner, Group: actual.Group,
		PrivateMode: actual.PrivateMode, PublicMode: actual.PublicMode, AllowDelete: deref(a.AllowDelete),
		PublicKey: actual.PublicKey, Fingerprint: actual.Fingerprint,
	}, nil
}

// Check defaults the comment to the resource's name, as the TypeScript constructor did.
func (SshKey) Check(ctx context.Context, req infer.CheckRequest) (infer.CheckResponse[SshKeyArgs], error) {
	args, failures, err := infer.DefaultCheck[SshKeyArgs](ctx, req.NewInputs)
	if err == nil && len(failures) == 0 && args.Comment == nil {
		name := req.Name
		args.Comment = &name
	}
	return infer.CheckResponse[SshKeyArgs]{Inputs: args, Failures: failures}, err
}

// Create generates the pair unless one is there; the id is the path.
func (SshKey) Create(ctx context.Context, req infer.CreateRequest[SshKeyArgs]) (infer.CreateResponse[SshKeyState], error) {
	a := req.Inputs
	if req.DryRun {
		return infer.CreateResponse[SshKeyState]{ID: a.Path, Output: SshKeyState{
			Path: a.Path, Type: orDefault(a.Type, "ed25519"), Comment: orDefault(a.Comment, a.Path),
			Owner: orDefault(a.Owner, "root"), Group: orDefault(a.Group, "root"), AllowDelete: deref(a.AllowDelete),
		}}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[SshKeyState]{}, err
	}
	// the comment falls back to the path, as in the TypeScript create
	state, err := settleSshKey(ctx, m, a, a.Path)
	if err != nil {
		return infer.CreateResponse[SshKeyState]{}, err
	}
	return infer.CreateResponse[SshKeyState]{ID: a.Path, Output: state}, nil
}

// Read reports the key; gone, it is forgotten and the next up generates a new one.
func (SshKey) Read(ctx context.Context, req infer.ReadRequest[SshKeyArgs, SshKeyState]) (infer.ReadResponse[SshKeyArgs, SshKeyState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[SshKeyArgs, SshKeyState]{}, err
	}
	actual, err := readSshKey(ctx, m, req.ID)
	if err != nil || actual == nil {
		return infer.ReadResponse[SshKeyArgs, SshKeyState]{}, err
	}
	state := req.State
	imported := state.Path == ""
	if imported {
		// nothing remembered: the algorithm and comment are the public key's own, so an imported RSA
		// pair is not labelled ed25519 and then replaced for it (review of #98)
		state.Type, state.Comment = keyTypeOf(actual.PublicKey), keyCommentOf(actual.PublicKey)
	}
	if state.Type == "" {
		state.Type = "ed25519"
	}
	state.Path, state.PublicKey, state.Fingerprint = req.ID, actual.PublicKey, actual.Fingerprint
	state.Owner, state.Group, state.PrivateMode, state.PublicMode = actual.Owner, actual.Group, actual.PrivateMode, actual.PublicMode
	inputs := req.Inputs
	if imported {
		inputs = SshKeyArgs{Path: req.ID, Type: &state.Type, Comment: &state.Comment, Owner: &state.Owner, Group: &state.Group}
	}
	return infer.ReadResponse[SshKeyArgs, SshKeyState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update keeps the key and corrects ownership and modes; type and comment apply only at generation.
func (SshKey) Update(ctx context.Context, req infer.UpdateRequest[SshKeyArgs, SshKeyState]) (infer.UpdateResponse[SshKeyState], error) {
	a := req.Inputs
	a.Path = req.ID
	if req.DryRun {
		s := req.State
		s.Owner, s.Group, s.AllowDelete = orDefault(a.Owner, "root"), orDefault(a.Group, "root"), deref(a.AllowDelete)
		return infer.UpdateResponse[SshKeyState]{Output: s}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[SshKeyState]{}, err
	}
	state, err := settleSshKey(ctx, m, a, req.State.Comment)
	return infer.UpdateResponse[SshKeyState]{Output: state}, err
}

// Diff: a different path or algorithm is a different key pair, created before the old is deleted.
func (SshKey) Diff(_ context.Context, req infer.DiffRequest[SshKeyArgs, SshKeyState]) (p.DiffResponse, error) {
	old, a := req.State, req.Inputs
	keyType := orDefault(a.Type, "ed25519")
	files := keyFiles{PrivateMode: old.PrivateMode, PublicMode: old.PublicMode, Owner: old.Owner, Group: old.Group}
	changed := old.Path != a.Path || old.Type != keyType ||
		permissionsWrong(files, orDefault(a.Owner, "root"), orDefault(a.Group, "root")) ||
		old.AllowDelete != deref(a.AllowDelete)
	var replaces []string
	if old.Path != a.Path || old.Type != keyType {
		replaces = []string{"path", "type"}
	}
	return diffOf(changed, false, replaces...), nil
}

// sshKeyDeleteRefusal is the refusal to delete a key nobody said could go.
func sshKeyDeleteRefusal(path string) error {
	return errors.New("refusing to delete the key at " + path + ": it may be authorised somewhere this stack does not " +
		"know about — another machine, a git host, a backup target — and removing it locks that " +
		"out with no way to tell in advance. Set allowDelete: true if you have checked, or remove " +
		"the resource from the program and leave the key on the machine.")
}

// Delete refuses unless allowDelete is set.
func (SshKey) Delete(ctx context.Context, req infer.DeleteRequest[SshKeyState]) (infer.DeleteResponse, error) {
	if !req.State.AllowDelete {
		return infer.DeleteResponse{}, sshKeyDeleteRefusal(req.ID)
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	_, err = m.must(ctx, m.escalate("rm -f "+shellQuote(req.ID)+" "+shellQuote(req.ID+".pub")))
	return infer.DeleteResponse{}, err
}
