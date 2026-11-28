package homelab

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf16"

	p "github.com/pulumi/pulumi-go-provider"
	"github.com/pulumi/pulumi-go-provider/infer"

	"github.com/antimatter-studios/pulumi-homelab/provider/authkeys"
	"github.com/antimatter-studios/pulumi-homelab/provider/jscompat"
	"github.com/antimatter-studios/pulumi-homelab/provider/mode"
)

// AuthorizedKey is one public key, authorised for one account. Ported from
// src/resources/authorizedkey.ts. The key body is the identity, not the line and not the comment;
// 0700 on ~/.ssh and 0600 on the file are set and read back, and ownership with them.
type AuthorizedKey struct{}

// AuthorizedKeyArgs are the inputs of an AuthorizedKey.
type AuthorizedKeyArgs struct {
	User    string   `pulumi:"user"`
	Key     string   `pulumi:"key"`
	Options []string `pulumi:"options,optional"`
	Path    *string  `pulumi:"path,optional"`
}

// AuthorizedKeyState is what is kept in state.
type AuthorizedKeyState struct {
	User          string   `pulumi:"user"`
	Key           string   `pulumi:"key"`
	Options       []string `pulumi:"options"`
	Path          string   `pulumi:"path"`
	Mode          string   `pulumi:"mode"`
	DirectoryMode string   `pulumi:"directoryMode"`
	Owner         string   `pulumi:"owner"`
	Comment       string   `pulumi:"comment"`
}

func (a *AuthorizedKeyArgs) Annotate(an infer.Annotator) {
	an.Describe(&a.User, "The account whose authorized_keys this is. It must already exist.")
	an.Describe(&a.Key, "The whole public key line as ssh-keygen prints it.")
	an.Describe(&a.Options, "Restrictions, written before the key: from=\"10.0.0.0/24\", command=\"…\", restrict.")
	an.Describe(&a.Path, "Where the file is, for an account whose home is not where getent says.")
}

func (s *AuthorizedKeyState) Annotate(an infer.Annotator) {
	an.Describe(&s.Mode, "0600, read back rather than assumed.")
	an.Describe(&s.DirectoryMode, "0700 on ~/.ssh, read back.")
	an.Describe(&s.Owner, "Who owns the file, read back.")
	an.Describe(&s.Comment, "The comment on the key as it sits in the file.")
}

func (r *AuthorizedKey) Annotate(a infer.Annotator) {
	a.Describe(&r, "A key that should be authorised, identified by the key itself rather than by what it is called.")
	a.SetToken("index", "AuthorizedKey")
}

const (
	authorizedFileMode      = "0600"
	authorizedDirectoryMode = "0700"
)

// keyBodyOrNull is keyBody with the TypeScript's null as "\x00", so two keys without a body compare
// equal the way null === null does.
func keyBodyOrNull(line string) string {
	if body, ok := authkeys.KeyBody(line); ok {
		return body
	}
	return "\x00"
}

func homeOf(ctx context.Context, m *machine, user string) (string, error) {
	asked, err := m.ask(ctx, "getent passwd "+shellQuote(user))
	if err != nil {
		return "", err
	}
	if asked.code != 0 {
		return "", fmt.Errorf("there is no account called %s on %s", user, m.describe())
	}
	fields := strings.Split(jscompat.Trim(asked.out), ":")
	home := ""
	if len(fields) > 5 {
		home = fields[5]
	}
	if home == "" {
		return "", fmt.Errorf("%s has no home directory, so it has nowhere to keep a key", user)
	}
	return home + "/.ssh/authorized_keys", nil
}

type authorizedRead struct {
	line          *string
	mode          string
	directoryMode string
	owner         string
}

const homelabMarker = "#pulumi-homelab#"

// authorizedContents distinguishes absence from a failed read before a read-modify-write.
func authorizedContents(ctx context.Context, m *machine, path string) (string, bool, error) {
	q := shellQuote(path)
	read, err := m.ask(ctx, m.escalate("if [ ! -e "+q+" ] && [ ! -L "+q+" ]; then exit 9; fi; cat -- "+q+" || exit 1"))
	if err != nil || read.code == 9 {
		return "", false, err
	}
	if read.code != 0 {
		return "", false, fmt.Errorf("could not read %s: %s", path, jscompat.Trim(read.err))
	}
	return read.out, true, nil
}

func authorizedReadCommand(path string) string {
	directory := parentOf(path)
	return "test -d " + shellQuote(directory) + " || exit 9; " +
		"stat -c '%a' " + shellQuote(directory) + "; " +
		"if [ -f " + shellQuote(path) + " ]; then stat -c '%a %U' " + shellQuote(path) + "; echo '#pulumi-homelab#'; " +
		"cat " + shellQuote(path) + "; else echo 'none none'; echo '#pulumi-homelab#'; fi"
}

// parseAuthorizedRead reads the answer to authorizedReadCommand.
func parseAuthorizedRead(out, key string) authorizedRead {
	head, contents := splitOnce(out, homelabMarker+"\n")
	headLines := append(strings.Split(jscompat.Trim(head), "\n"), "", "")
	directoryMode, fileLine := headLines[0], headLines[1]
	words := append(jscompat.Split(jscompat.Trim(fileLine)), "", "")
	fileMode, fileOwner := words[0], words[1]
	read := authorizedRead{directoryMode: mode.NormaliseMode(directoryMode)}
	if body, ok := authkeys.KeyBody(key); ok {
		for _, existing := range strings.Split(contents, "\n") {
			if b, ok := authkeys.KeyBody(existing); ok && b == body {
				line := existing
				read.line = &line
				break
			}
		}
	}
	if fileMode != "none" {
		read.mode = mode.NormaliseMode(fileMode)
	}
	if fileOwner != "none" {
		read.owner = fileOwner
	}
	return read
}

func readAuthorizedKey(ctx context.Context, m *machine, path, key string) (*authorizedRead, error) {
	asked, err := m.ask(ctx, m.escalate(authorizedReadCommand(path)))
	if err != nil {
		return nil, err
	}
	if asked.code == 9 {
		return nil, nil
	}
	if asked.code != 0 {
		return nil, fmt.Errorf("could not read %s: %s", path, jscompat.Trim(asked.err))
	}
	read := parseAuthorizedRead(asked.out, key)
	return &read, nil
}

// splitAuthorized splits a line from the file into its options and the key, so an import declares
// exactly what is there. The key starts at the type word before the body.
func splitAuthorized(text string) (options []string, key string) {
	text = jscompat.Trim(text)
	body, ok := authkeys.KeyBody(text)
	if !ok {
		return []string{}, text
	}
	at := strings.Index(text, body)
	start := strings.LastIndexFunc(strings.TrimRightFunc(text[:at], jscompat.IsSpace), jscompat.IsSpace) + 1
	if strings.HasPrefix(text[start:], body) || start >= at {
		start = at
	}
	key = text[start:]
	prefix := jscompat.Trim(text[:start])
	options = []string{}
	if prefix != "" {
		// commas inside double quotes belong to the option (command="a,b")
		quoted, from := false, 0
		for i, r := range prefix {
			switch {
			case r == '"':
				quoted = !quoted
			case r == ',' && !quoted:
				options = append(options, prefix[from:i])
				from = i + 1
			}
		}
		options = append(options, prefix[from:])
	}
	if authkeys.AuthorizedLine(key, options) != text {
		return []string{}, text
	}
	return options, key
}

// authorizedWriteCommand puts the file in place with both modes and the owner, every time.
func authorizedWriteCommand(path, user, content string) string {
	directory := parentOf(path)
	return "mkdir -p " + shellQuote(directory) + " && " +
		heredoc(path, content) + "\n" +
		"chmod " + authorizedDirectoryMode + " " + shellQuote(directory) + " && chmod " + authorizedFileMode + " " + shellQuote(path) + " && " +
		"chown -R " + shellQuote(user) + " " + shellQuote(directory)
}

func settleAuthorizedKey(ctx context.Context, m *machine, a AuthorizedKeyArgs) (AuthorizedKeyState, error) {
	if _, ok := authkeys.KeyBody(a.Key); !ok {
		return AuthorizedKeyState{}, fmt.Errorf("that does not look like a public key: expected '<type> AAAA… <comment>', got '%s'", jsSlice(a.Key, 40))
	}
	path := ""
	if a.Path != nil {
		path = *a.Path
	} else {
		home, err := homeOf(ctx, m, a.User)
		if err != nil {
			return AuthorizedKeyState{}, err
		}
		path = home
	}
	options := a.Options
	if options == nil {
		options = []string{}
	}
	line := authkeys.AuthorizedLine(a.Key, options)

	before, err := readAuthorizedKey(ctx, m, path, a.Key)
	if err != nil {
		return AuthorizedKeyState{}, err
	}
	settled := before != nil && before.line != nil && *before.line == line &&
		before.mode == authorizedFileMode && before.directoryMode == authorizedDirectoryMode && before.owner == a.User
	if !settled {
		current, _, err := authorizedContents(ctx, m, path)
		if err != nil {
			return AuthorizedKeyState{}, err
		}
		if _, err := m.must(ctx, m.escalate(authorizedWriteCommand(path, a.User, authkeys.UpsertAuthorized(current, a.Key, line)))); err != nil {
			return AuthorizedKeyState{}, err
		}
	}
	actual, err := readAuthorizedKey(ctx, m, path, a.Key)
	if err != nil {
		return AuthorizedKeyState{}, err
	}
	if actual == nil || actual.line == nil {
		return AuthorizedKeyState{}, fmt.Errorf("wrote the key for %s but it is not in %s", a.User, path)
	}
	return AuthorizedKeyState{
		User: a.User, Key: a.Key, Options: options, Path: path,
		Mode: actual.mode, DirectoryMode: actual.directoryMode, Owner: actual.owner,
		Comment: authkeys.KeyComment(*actual.line),
	}, nil
}

// jsSlice is String.prototype.slice(0, n), which counts UTF-16 code units. A surrogate pair cut in
// half leaves a lone surrogate in JavaScript; Go cannot hold one, so it becomes U+FFFD.
func jsSlice(s string, n int) string {
	units := utf16.Encode([]rune(s))
	if len(units) <= n {
		return s
	}
	return string(utf16.Decode(units[:n]))
}

func authorizedDryRun(a AuthorizedKeyArgs) AuthorizedKeyState {
	options := a.Options
	if options == nil {
		options = []string{}
	}
	return AuthorizedKeyState{User: a.User, Key: a.Key, Options: options, Path: deref(a.Path)}
}

// Create authorises the key; the id is `<path>#<body>`.
func (AuthorizedKey) Create(ctx context.Context, req infer.CreateRequest[AuthorizedKeyArgs]) (infer.CreateResponse[AuthorizedKeyState], error) {
	if req.DryRun {
		s := authorizedDryRun(req.Inputs)
		return infer.CreateResponse[AuthorizedKeyState]{ID: s.Path + "#" + keyBodyOrNull(s.Key), Output: s}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.CreateResponse[AuthorizedKeyState]{}, err
	}
	state, err := settleAuthorizedKey(ctx, m, req.Inputs)
	if err != nil {
		return infer.CreateResponse[AuthorizedKeyState]{}, err
	}
	body, _ := authkeys.KeyBody(state.Key)
	return infer.CreateResponse[AuthorizedKeyState]{ID: state.Path + "#" + body, Output: state}, nil
}

// Read reports the line and the modes; the line or the whole .ssh gone, it is forgotten.
func (AuthorizedKey) Read(ctx context.Context, req infer.ReadRequest[AuthorizedKeyArgs, AuthorizedKeyState]) (infer.ReadResponse[AuthorizedKeyArgs, AuthorizedKeyState], error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.ReadResponse[AuthorizedKeyArgs, AuthorizedKeyState]{}, err
	}
	state := req.State
	imported := state.Key == ""
	path, body := splitOnce(req.ID, "#")
	if state.Path == "" {
		state.Path = path
	}
	if state.Options == nil {
		state.Options = []string{}
	}
	// an import has no remembered key, so the body in the id is what is looked for
	lookFor := state.Key
	if imported {
		lookFor = body
	}
	actual, err := readAuthorizedKey(ctx, m, state.Path, lookFor)
	if err != nil || actual == nil || actual.line == nil {
		return infer.ReadResponse[AuthorizedKeyArgs, AuthorizedKeyState]{}, err
	}
	state.Mode, state.DirectoryMode, state.Owner = actual.mode, actual.directoryMode, actual.owner
	state.Comment = authkeys.KeyComment(*actual.line)
	inputs := req.Inputs
	if imported {
		state.Options, state.Key = splitAuthorized(*actual.line)
		state.User = actual.owner
		inputs = AuthorizedKeyArgs{User: state.User, Key: state.Key, Options: state.Options, Path: &state.Path}
	}
	return infer.ReadResponse[AuthorizedKeyArgs, AuthorizedKeyState]{ID: req.ID, Inputs: inputs, State: state}, nil
}

// Update settles the key again.
func (AuthorizedKey) Update(ctx context.Context, req infer.UpdateRequest[AuthorizedKeyArgs, AuthorizedKeyState]) (infer.UpdateResponse[AuthorizedKeyState], error) {
	if req.DryRun {
		s := authorizedDryRun(req.Inputs)
		if s.Path == "" {
			s.Path = req.State.Path
		}
		return infer.UpdateResponse[AuthorizedKeyState]{Output: s}, nil
	}
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.UpdateResponse[AuthorizedKeyState]{}, err
	}
	state, err := settleAuthorizedKey(ctx, m, req.Inputs)
	return infer.UpdateResponse[AuthorizedKeyState]{Output: state}, err
}

// Diff: a different key, or the same key for a different account, is a different authorisation.
func (AuthorizedKey) Diff(_ context.Context, req infer.DiffRequest[AuthorizedKeyArgs, AuthorizedKeyState]) (p.DiffResponse, error) {
	old, a := req.State, req.Inputs
	options := a.Options
	if options == nil {
		options = []string{}
	}
	keyChanged := keyBodyOrNull(old.Key) != keyBodyOrNull(a.Key)
	changed := keyChanged || joined(old.Options) != joined(options) || old.User != a.User ||
		old.Mode != authorizedFileMode || old.DirectoryMode != authorizedDirectoryMode || old.Owner != a.User
	var replaces []string
	if keyChanged || old.User != a.User {
		replaces = []string{"key", "user"}
	}
	return diffOf(changed, true, replaces...), nil
}

// Delete takes out only this key's line and leaves every other key in the file alone.
func (AuthorizedKey) Delete(ctx context.Context, req infer.DeleteRequest[AuthorizedKeyState]) (infer.DeleteResponse, error) {
	m, err := machineFrom(ctx)
	if err != nil {
		return infer.DeleteResponse{}, err
	}
	path := req.State.Path
	if path == "" {
		path, _ = splitOnce(req.ID, "#")
	}
	current, exists, err := authorizedContents(ctx, m, path)
	if err != nil || !exists {
		return infer.DeleteResponse{}, err
	}
	if updated := authkeys.RemoveAuthorized(current, req.State.Key); updated != current {
		_, err = m.feed(ctx, "cat > "+shellQuote(path), []byte(updated))
	}
	return infer.DeleteResponse{}, err
}
