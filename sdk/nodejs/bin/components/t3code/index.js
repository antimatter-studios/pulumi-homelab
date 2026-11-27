"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.t3codeResource = exports.AGENT_DEFAULTS = exports.T3_DEFAULT = exports.unitResourceLines = void 0;
const archive_1 = require("../../archive");
const officialInstall_1 = require("../../officialInstall");
const aptPackages_1 = require("../../aptPackages");
const directory_1 = require("../../directory");
const managedFile_1 = require("../../managedFile");
const sshKey_1 = require("../../sshKey");
const sudoRule_1 = require("../../sudoRule");
const symlink_1 = require("../../symlink");
const systemdUnit_1 = require("../../systemdUnit");
const user_1 = require("../../user");
const isOfficial = (spec) => 'installer' in spec;
/**
 * The `[Service]` lines for the limits and `TMPDIR`, or an empty string when neither is set, so a
 * unit that asks for nothing is byte for byte what it was before these options existed.
 */
function unitResourceLines(unit = {}) {
    var _a;
    const lines = [];
    const limits = (_a = unit.limits) !== null && _a !== void 0 ? _a : {};
    for (const [name, value] of [['CPUWeight', limits.cpuWeight], ['IOWeight', limits.ioWeight]]) {
        if (value !== undefined && (!Number.isInteger(value) || value < 1 || value > 10000)) {
            throw new Error(`${name} must be an integer from 1 to 10000, not ${value}`);
        }
    }
    if (limits.memoryHigh)
        lines.push(`MemoryHigh=${limits.memoryHigh}`);
    if (limits.memoryMax)
        lines.push(`MemoryMax=${limits.memoryMax}`);
    if (limits.memorySwapMax)
        lines.push(`MemorySwapMax=${limits.memorySwapMax}`);
    if (limits.oomPolicy)
        lines.push(`OOMPolicy=${limits.oomPolicy}`);
    if (limits.cpuWeight !== undefined)
        lines.push(`CPUWeight=${limits.cpuWeight}`);
    if (limits.ioWeight !== undefined)
        lines.push(`IOWeight=${limits.ioWeight}`);
    if (unit.tmpDir)
        lines.push(`Environment=TMPDIR=${unit.tmpDir}`);
    if (!lines.length)
        return '';
    return `\n# What the agents may take of the machine, and where their scratch goes. Declared by the stack.\n${lines.join('\n')}\n`;
}
exports.unitResourceLines = unitResourceLines;
const DEFAULT_ACCOUNT = 't3code';
/**
 * Nightly, and that is forced rather than chosen.
 *
 * Preview builds are never offered as updates, so a preview install would sit on its bootstrap build
 * for ever — the exact outcome that not managing versions is meant to avoid. Stable publishes no
 * linux-arm64 asset at all, which is why this was forced on the Raspberry Pi; the x86_64 server it
 * runs on now (2026-11-26) keeps the same channel.
 *
 * The checksum is from the release's own SHA256SUMS and recomputed from the downloaded file, so it
 * pins what was meant rather than merely what was got: https authenticates the server, not the
 * artefact, and a release asset can be replaced under a fixed url.
 */
exports.T3_DEFAULT = {
    // The build the Pi was running when it was retired (2026-11-25), so the move to x86 is not also
    // a downgrade: its data had been migrated by this version, and an older one may not read it.
    version: '0.1.13-nightly.20261125.5245',
    sha256: 'de331775c0ef436edf446021c05b92d21787424efe5e8b33699c04710f175152',
    url: (version) => `https://github.com/pingdotgg/t3code/releases/download/v${version}/t3-${version}-linux-x64.tar.gz`,
};
exports.AGENT_DEFAULTS = [
    {
        /**
         * Not a provider, and not optional either. T3 shells out to `git` for clone, push and pull
         * and supplies no credential of its own — it sets `GIT_ASKPASS=""` and
         * `GIT_TERMINAL_PROMPT=0` precisely so a missing one fails immediately instead of hanging a
         * subprocess for ever. Everything API-shaped goes through `gh`, so without it the GitHub
         * integration does not work at all, whatever git credentials exist.
         *
         * It is also the credential helper named in the gitconfig above, which is why one token
         * serves both the API and the transport rather than needing two.
         */
        key: 'gh', bin: 'bin/gh',
        url: 'https://github.com/cli/cli/releases/download/v2.106.0/gh_2.106.0_linux_amd64.tar.gz',
        // From the release's own gh_2.100.0_checksums.txt, reverified against the fetched file.
        sha256: 'a476449fa9625413c54a3734eded62e9e480e49f421ff08e7f3b3879c059a033',
        strip: 1,
    },
    /**
     * The three coding agents, the vendors' way. They ship every few days and update themselves, and
     * T3 refuses versions it has outgrown; a pin here was out of date within the week, and the tools'
     * own `update` could not replace a copy the deployment owned.
     */
    { key: 'claude', installer: 'curl -fsSL https://claude.ai/install.sh | bash', bin: '.local/bin/claude' },
    {
        // the standalone installer, with companion binaries (codex-code-mode-host) beside it
        key: 'codex', installer: 'curl -fsSL https://chatgpt.com/codex/install.sh | CODEX_NON_INTERACTIVE=1 sh',
        bin: '.local/bin/codex',
    },
    {
        // installs to ~/.opencode/bin and would edit .bashrc to find it; the link replaces that
        key: 'opencode', installer: 'curl -fsSL https://opencode.ai/install | bash -s -- --no-modify-path',
        bin: '.opencode/bin/opencode', link: true,
    },
];
/**
 * T3 Code: an agent harness, run headless so work outlives the laptop that started it.
 *
 * It wraps coding-agent CLIs as child processes and exposes them to web, desktop and mobile
 * clients. The point of putting it here rather than on a laptop is that a long task then survives a
 * closed lid, a flat phone, and a train going into a tunnel.
 *
 * Two things about the unit, both silent failures rather than loud ones:
 *
 *   1. `ExecStart` must run the launcher at `~/.local/bin/t3`, never a versioned path under
 *      `~/.t3/runtime/versions/`. The launcher resolves to the newest install; a versioned path
 *      pins the service to whatever was bootstrapped, so the program self-updates for ever while
 *      the service keeps running the original build — and looks perfectly healthy doing it.
 *
 *   2. A self-update does not reach a service somebody else started. `t3 update` restarts its own
 *      background service and deliberately leaves ours alone, printing that it is still on the old
 *      version. So picking up an update is `systemctl restart <unit>`, which wants to be a task
 *      somebody can run without a deployment.
 *
 * And one thing that must never happen on a machine described this way: `t3 service install`. It writes a user unit
 * under `~/.config/systemd/user` and needs lingering enabled. We declare a *system* unit with
 * `User=<account>` instead, which gets boot-start and logout-survival without lingering — and running
 * its installer would leave two owners of one service, which is the failure that had
 * a service rewritten on every deployment for weeks before anyone noticed.
 */
function t3codeResource(provider, args) {
    var _a, _b, _c, _d, _e, _f, _g, _h, _j, _k, _l, _m, _o, _p, _q, _r, _s;
    const account_ = (_a = args.account) !== null && _a !== void 0 ? _a : DEFAULT_ACCOUNT;
    // Resource names that existed before a second account was possible keep their old form for the
    // default account, so its state is not replaced; any other account prefixes them with its name.
    const named = (base) => (account_ === DEFAULT_ACCOUNT ? base : `${account_}-${base}`);
    const HOME = args.home;
    const WORKSPACE = args.workspace;
    const unitName = (_c = (_b = args.unit) === null || _b === void 0 ? void 0 : _b.name) !== null && _c !== void 0 ? _c : account_;
    /**
     * The account, isolated on purpose.
     *
     * It runs agents, and agents run whatever they are asked to. So it is not root, it is not in
     * `rwgroup`, and it has no access to the cluster — the blast radius of a confused agent is meant
     * to be its own workspace and nothing else. Chris chose this over running as himself knowing it
     * might need adjusting: "we can always fix it if things don't work".
     *
     * It does have a real shell, and that is not an oversight. An agent harness shells out constantly;
     * `nologin` here would not be hardening, it would be a service that cannot do its job.
     *
     * The home is on the array rather than under /home, and that is the single most important line in
     * this file. `~/.t3/userdata` is a SQLite database under continuous write — every thread, every
     * message. On the SD card that is a card with a countdown on it, and this machine has already lost
     * one that way.
     */
    const account = new user_1.User(`user-${account_}`, {
        name: account_,
        shell: '/bin/bash',
        home: HOME,
        createHome: true,
        // NOT `rwgroup`. That is how everything else on the array is shared, and leaving this account
        // out of it is exactly what confines it: it can write what it owns and read what is
        // world-readable, and nothing else on a 3.7T pool of somebody's photographs and music.
        //
        // `kvm` is here because /dev/kvm is `crw-rw---- root:kvm`, so hardware virtualisation is a
        // group membership rather than a permission to be granted at runtime. An agent reached for
        // `sudo setfacl -m u:t3code:rw /dev/kvm` instead and was refused, which was the right outcome:
        // a device ACL applied by hand survives until something recreates the node and then silently
        // does not, and nothing records why it was there. Declared membership is the same access,
        // permanently, and visible in the description of the machine.
        groups: (_d = args.groups) !== null && _d !== void 0 ? _d : [],
        uid: args.uid,
        group: args.primaryGroup ? account_ : undefined,
    }, { provider, dependsOn: [...(args.primaryGroup ? [args.primaryGroup] : []), ...((_e = args.accountDependsOn) !== null && _e !== void 0 ? _e : [])] });
    /**
     * Its home, stated so the permissions are a decision rather than whatever `useradd` defaulted to.
     *
     * `0700`, and no group. This directory holds OAuth sessions for four services — the harness's own
     * relay credential and one per coding agent. They are renewable and revocable, which is why they
     * are not in the vault, but they are still live credentials and there is no reason for anyone but
     * this account to read them. Everything else on this array is deliberately group-readable; this is
     * the one place where that would be wrong.
     */
    new directory_1.Directory(`dir-${account_}-home`, {
        path: HOME,
        owner: account_,
        group: account_,
        mode: '0700',
    }, { provider, dependsOn: [account] });
    /**
     * What an agent needs to exist before it can do anything useful.
     *
     * `git` is not optional — the harness's whole workflow is cloning a repository and working in it.
     * The rest are the tools a coding agent reaches for without being told, and their absence shows up
     * as an agent failing at something unrelated rather than as a missing package.
     */
    new aptPackages_1.AptPackages(`pkg-${account_}-tools`, {
        names: ['git', 'curl', 'ca-certificates', 'unzip'],
    }, { provider });
    const BIN = `${HOME}/.local/bin`;
    const OPT = `${HOME}/.local/opt`;
    /**
     * `~/.local/bin`, which Debian's own `.profile` puts on PATH — but only if it exists.
     *
     * That conditional is why this is declared rather than left to the installs to create: the four
     * login commands are run in a login shell, and a directory created after `.profile` was sourced
     * is not on the PATH of the shell that created it.
     */
    /**
     * The directories the installs land *inside*, declared because otherwise root creates them.
     *
     * `Archive` unpacks with escalation and makes missing parents with `mkdir -p`, which means every
     * intermediate directory is root-owned even when the install itself is not. That is not cosmetic:
     * `t3 update` unpacks each new version into `.t3/runtime/versions/<version>`, so a root-owned
     * `versions` directory means the account can never create one — the update fails, the launcher
     * keeps pointing at the bootstrap build, and the install goes on verifying green. `.t3` itself
     * holds `userdata`, the SQLite database written on every message.
     *
     * The failure is invisible from the install's side, which is exactly why it is worth declaring
     * these rather than letting them be a side effect of whatever ran first.
     */
    const dotlocal = new directory_1.Directory(`dir-${account_}-dotlocal`, {
        path: `${HOME}/.local`, owner: account_, group: account_, mode: '0755',
    }, { provider, dependsOn: [account] });
    // 0700: this holds userdata and the harness's own credentials.
    const dott3 = new directory_1.Directory(`dir-${account_}-dott3`, {
        path: `${HOME}/.t3`, owner: account_, group: account_, mode: '0700',
    }, { provider, dependsOn: [account] });
    const runtime = new directory_1.Directory(`dir-${account_}-runtime`, {
        path: `${HOME}/.t3/runtime`, owner: account_, group: account_, mode: '0755',
    }, { provider, dependsOn: [dott3] });
    const versions = new directory_1.Directory(`dir-${account_}-versions`, {
        path: `${HOME}/.t3/runtime/versions`, owner: account_, group: account_, mode: '0755',
    }, { provider, dependsOn: [runtime] });
    const bin = new directory_1.Directory(`dir-${account_}-bin`, {
        path: BIN, owner: account_, group: account_, mode: '0755',
    }, { provider, dependsOn: [dotlocal] });
    const opt = new directory_1.Directory(`dir-${account_}-opt`, {
        path: OPT, owner: account_, group: account_, mode: '0755',
    }, { provider, dependsOn: [dotlocal] });
    /**
     * All four are owned by `t3code`, and that is the field the whole arrangement rests on.
     *
     * `Archive` unpacks with escalation, so without `owner` the tree lands root-owned — and a
     * root-owned binary cannot be replaced by `claude update`, `opencode upgrade` or `t3 update`. The
     * install would verify green forever while the software silently never updated again, which is a
     * worse failure than an install that fails, because nothing ever reports it.
     *
     * None of these are pinned in the sense of being *held* at a version. `Archive` asks only whether
     * the thing is installed and never compares versions, so a self-update does not read as drift and
     * will not be undone. The checksum pins what is fetched onto a bare machine the first time, which
     * is a statement about provenance and not about version policy: https authenticates the server,
     * not the artefact, and a release asset can be replaced under a fixed url.
     */
    const t3version = (_g = (_f = args.t3) === null || _f === void 0 ? void 0 : _f.version) !== null && _g !== void 0 ? _g : exports.T3_DEFAULT.version;
    const t3prefix = `${HOME}/.t3/runtime/versions/${t3version}`;
    /**
     * Nightly, and that is forced rather than chosen. Preview builds are never offered as updates, so
     * a preview install would sit on its bootstrap build for ever — the exact outcome that not
     * managing versions is meant to avoid. Stable publishes no linux-arm64 asset at all.
     *
     * It unpacks into `.t3/runtime/versions/<version>`, which is where `t3 update` puts new versions
     * too. Anywhere else and the bootstrap and every later self-update would live in separate trees,
     * with the launcher walking away from what Pulumi installed.
     */
    const t3 = new archive_1.Archive(named('archive-t3'), {
        name: 't3',
        url: ((_j = (_h = args.t3) === null || _h === void 0 ? void 0 : _h.url) !== null && _j !== void 0 ? _j : exports.T3_DEFAULT.url)(t3version),
        // From the release's own SHA256SUMS, and recomputed from the downloaded file. So it pins what
        // was meant, not merely what was got.
        sha256: (_l = (_k = args.t3) === null || _k === void 0 ? void 0 : _k.sha256) !== null && _l !== void 0 ? _l : exports.T3_DEFAULT.sha256,
        prefix: t3prefix,
        strip: 1,
        binary: 't3',
        // Not `bin/t3`. The executable sits at the top of the tree, beside a real node_modules and a
        // resource-monitor helper — so the whole tree has to land together. This is the installer's own
        // final check, which makes it the vendor's definition of a good install.
        healthCommand: '{}/t3 --version',
        versionCommand: '{}/t3 --version',
        owner: account_,
        group: account_,
    }, { provider, dependsOn: [account, versions] });
    /**
     * The marker t3's own tooling looks for. Without it a version directory can read as a half-
     * finished install — cheap to write, unpleasant to diagnose.
     */
    new managedFile_1.ManagedFile(named('file-t3-install-complete'), {
        path: `${t3prefix}/.install-complete`,
        content: `${t3version}\n`,
        owner: account_,
        group: account_,
        mode: '0644',
    }, { provider, dependsOn: [t3] });
    /**
     * The launcher, created once and then deliberately never enforced.
     *
     * `ignoreChanges: ['target']` is doing real work here. `t3 update` unpacks a new version and
     * **repoints this symlink** — that is how a self-update takes effect. A resource that owned the
     * target would see the new version as drift and point it back at the bootstrap build on the next
     * deployment, silently undoing the update while reporting success and leaving the newer build
     * unused on disk. That is the double-owner shape that had `jonflix.service` rewritten on every
     * deployment for weeks, except here the second writer is the software itself.
     *
     * So the declared property is "a launcher exists at this path", never "it points here". Which is
     * also why the unit's ExecStart must name this path and not a versioned one: a versioned ExecStart
     * pins the service to whatever was bootstrapped while the program updates around it for ever.
     */
    new symlink_1.Symlink(named('link-t3'), {
        path: `${BIN}/t3`,
        target: `${t3prefix}/t3`,
        // t3 1.13+ updates itself by writing a launcher script here instead of repointing the link
        acceptLauncherScript: true,
    }, { provider, dependsOn: [t3, bin], ignoreChanges: ['target'] });
    /**
     * The three provider CLIs t3 spawns as child processes, and `gh`.
     *
     * `mise` is NOT here: it is declared once in toolchain.ts and shared by every account, because
     * Pulumi owns its version and it therefore has no need to write its own install directory. Its
     * shims still land per-account under `~/.local/share/mise`, which is why they stay on the PATH
     * below — the manager is shared, the tools it installs are not.
     *
     * They have to be on PATH for t3 to find them at all, and they are installed the same way and for
     * the same reasons as t3 itself. Unlike t3's launcher these symlinks point at a fixed path inside
     * a fixed prefix — each updater rewrites the binary in place rather than repointing a link — so
     * there is nothing here for a self-update to fight over.
     */
    const clis = (_m = args.agents) !== null && _m !== void 0 ? _m : exports.AGENT_DEFAULTS;
    for (const cli of clis) {
        if (isOfficial(cli)) {
            const installed = new officialInstall_1.OfficialInstall(named(`install-${cli.key}`), {
                account: account_,
                home: HOME,
                installer: cli.installer,
                bin: `${HOME}/${cli.bin}`,
            }, { provider, dependsOn: [account, bin] });
            if (cli.link) {
                new symlink_1.Symlink(named(`link-${cli.key}`), {
                    path: `${BIN}/${cli.key}`,
                    target: `${HOME}/${cli.bin}`,
                }, { provider, dependsOn: [installed] });
            }
            continue;
        }
        const archive = new archive_1.Archive(named(`archive-${cli.key}`), {
            name: cli.key,
            url: cli.url,
            sha256: cli.sha256,
            prefix: `${OPT}/${cli.key}`,
            strip: cli.strip,
            binary: cli.bin,
            healthCommand: `{}/${cli.bin} --version`,
            versionCommand: `{}/${cli.bin} --version`,
            owner: account_,
            group: account_,
        }, { provider, dependsOn: [account, opt] });
        new symlink_1.Symlink(named(`link-${cli.key}`), {
            path: `${BIN}/${cli.key}`,
            target: `${OPT}/${cli.key}/${cli.bin}`,
        }, { provider, dependsOn: [archive, bin] });
    }
    /**
     * Git, so work done here can leave the machine.
     *
     * A harness that can write code and cannot push it is a harness whose output lives on one SD-less
     * NVMe in a cupboard. Three things are missing on a fresh account and all three are declared here:
     * an identity for commits, a key to authenticate with, and somewhere for ssh to keep both.
     */
    const dotssh = new directory_1.Directory(`dir-${account_}-dotssh`, {
        path: `${HOME}/.ssh`,
        owner: account_,
        group: account_,
        // ssh refuses to use a key whose directory is group- or world-readable, and says so obscurely.
        mode: '0700',
    }, { provider, dependsOn: [account] });
    /**
     * The key, generated on the machine so that nothing secret is ever carried anywhere.
     *
     * `SshKey` runs `ssh-keygen` on the host and exports only the public key and the fingerprint. The
     * private half is written once, by the machine, into a directory only this account can read, and
     * never enters Pulumi state, this session, or a clipboard. That is the whole point of declaring it
     * rather than making one here and copying it over: a private key that no human has ever held
     * cannot have been leaked by one.
     *
     * What it can reach is a decision left to whoever installs the public half, and it is a real one.
     * Added to a GitHub account it grants push to everything that account can push to; added as a
     * per-repository deploy key it grants exactly one repository. Agents run whatever they are asked
     * to, so the difference matters more here than it would for a person's own key.
     */
    const key = new sshKey_1.SshKey(`sshkey-${account_}-git`, {
        path: `${HOME}/.ssh/id_ed25519`,
        type: 'ed25519',
        comment: (_o = args.git.keyComment) !== null && _o !== void 0 ? _o : `${account_}@${(_q = (_p = args.unit) === null || _p === void 0 ? void 0 : _p.name) !== null && _q !== void 0 ? _q : 'homelab'}`,
        // Without these the pair is generated root-owned, ssh refuses to use a private key it cannot
        // confirm is the caller's, and the resource reports created for a key that cannot authenticate.
        owner: account_,
        group: account_,
    }, { provider, dependsOn: [dotssh] });
    /**
     * The identity commits carry, and one protection that would otherwise stop git dead.
     *
     * Without `user.name` and `user.email` git either refuses to commit or invents
     * `t3code@homelab`, which is nobody and links to no account. These are Chris's own, so work done
     * by the harness is attributed the same way work done on his laptop is — the machine that ran it
     * is an implementation detail, not an author.
     *
     * `safe.directory` is the one that would look like a bug. Git refuses to operate on a repository
     * owned by a different user — "detected dubious ownership" — which is exactly what every
     * pre-existing checkout under /mnt/storage/projects is, since they belong to `chris`. Without
     * this, reading them fails in a way that reads as a permissions fault rather than as a deliberate
     * refusal. It widens nothing: the file permissions still decide what can be written, and this only
     * stops git second-guessing them.
     */
    new managedFile_1.ManagedFile(`file-${account_}-gitconfig`, {
        path: `${HOME}/.gitconfig`,
        owner: account_,
        group: account_,
        mode: '0644',
        content: args.git.config,
    }, { provider, dependsOn: [account] });
    /**
     * Root, said plainly rather than dressed as an allowlist.
     *
     * This began as a list of permitted commands and grew to sixteen as agents hit its edges: apt,
     * then systemctl, then the ext4 tooling. That list was never a boundary. `apt-get` runs a
     * package's maintainer scripts as root, so from the first grant this account could do anything
     * root can do — the list constrained the spelling, not the consequence.
     *
     * Which left the worst of both: no protection, and an agent stopped several times a day by a
     * rule that could not have protected anything. Each stop cost a round trip through Chris, and
     * every one was resolved by widening the list, because there was never a principled reason to
     * refuse.
     *
     * So it says what is true. Chris asked for an account that can install things and manage
     * services; this is that, without a list implying a limit that does not exist.
     *
     * **What still holds, and it is not nothing.** The confinement that matters here was never the
     * sudoers file — it is that the account is outside `rwgroup`, so the 3.7T pool is not its to
     * write; that its workspace is one directory with a named-user ACL and a sticky bit; and that
     * every credential it holds is its own rather than Chris's login. Those are enforced by the
     * kernel and unaffected by this.
     *
     * **What is now on trust.** An agent that wanted to could take the machine. That is the shape of
     * the thing Chris chose when he chose an agent that installs its own dependencies, and it is
     * better stated than implied.
     *
     * The honest way back is not a shorter list. It is package installation returning to Pulumi and
     * this file disappearing, which is what everything else on this machine looks like.
     */
    // `sudo: false` is an account with no root at all: a person's own agents, rather than the machine's.
    if (args.sudo !== false)
        new sudoRule_1.SudoRule(`sudo-${account_}-apt`, {
            file: (_s = (_r = args.sudo) === null || _r === void 0 ? void 0 : _r.file) !== null && _s !== void 0 ? _s : `030_${account_}`,
            user: account_,
            runAs: 'root',
            passwordless: true,
            // No `commands`, so: ALL. Also fixes a refusal a list cannot: sudoers strips the environment,
            // so `DEBIAN_FRONTEND=noninteractive apt-get install` was rejected for the VARIABLE while the
            // command itself was permitted — apt then waits on a debconf prompt nobody will answer.
        }, { provider, dependsOn: [account] });
    /**
     * The service, so a long task outlives the laptop that started it.
     *
     * **A system unit with `User=t3code`, never the user unit `t3 service install` writes.** That
     * installer puts a unit under `~/.config/systemd/user` and needs lingering enabled for it to
     * survive logout. This gets boot-start and logout-survival without lingering, and more
     * importantly running the installer as well would leave two owners of one service — which is the
     * failure that had `jonflix.service` rewritten on every deployment for weeks before anybody
     * noticed. `t3 connect` reports that it cannot reach the systemd user manager, and that message
     * is correct and expected: there is no user manager because we do not want one.
     *
     * **ExecStart names the launcher and never a versioned path.** `~/.local/bin/t3` is repointed by
     * `t3 update`; a path under `.t3/runtime/versions/` would pin the service to whatever was
     * bootstrapped, so the program would self-update for ever while the service kept running the
     * original build — and looked perfectly healthy doing it.
     *
     * **Do not diagnose this with `t3 service status`, whatever the program tells you to do.** Both
     * `t3 connect status` and the troubleshooting docs end by pointing at it, and here it is actively
     * misleading: it inspects t3's own systemd *user* service, which this machine deliberately does
     * not have, so it reports `user-manager-unavailable` and reads as a fault when nothing is wrong.
     * The equivalents for a unit somebody else owns are `systemctl status ${unitName}` and
     * `journalctl -u ${unitName}`. Likewise `t3 service restart` restarts a service that is not this one.
     *
     * Error signatures worth knowing: `auth_invalid` or `invalid_bearer` means `t3 connect login` and
     * then `systemctl restart ${unitName}`; `environment_link_limit_exceeded` means too many environments
     * registered on the account; an expired link proof is worth checking the clock for, on a machine
     * with no RTC battery and a history of unclean power loss.
     *
     * **A self-update does not reach a service somebody else started.** `t3 update` restarts its own
     * background service and deliberately leaves this one alone, printing that it is still on the old
     * version. Picking up an update is `sudo systemctl restart ${unitName}`, which wants to be something
     * runnable without a deployment.
     */
    new systemdUnit_1.SystemdUnit(`unit-${account_}`, {
        name: unitName,
        enabled: true,
        started: true,
        unit: `# /etc/systemd/system/${unitName}.service
# Managed by Pulumi, from the homelab-server stack. Do not run \`t3 service install\`: it writes a
# competing user unit and this file would then be one of two owners of the same service.
[Unit]
Description=T3 Code, headless agent harness
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=${account_}
Group=${account_}
# Where the add-project dialog proposes to clone is the server setting \`addProjectBaseDirectory\`,
# surfaced in the UI as "Add project base directory". \`~/\` is its EMPTY fallback, not a derivation
# from HOME -- so it is unconfigured rather than unconfigurable. It is deliberately not declared
# here: it persists in ~/.t3/userdata/settings.json alongside every other setting the app writes,
# so a ManagedFile owning that path would revert Chris's UI changes on each deployment with
# nothing to connect the symptom to the cause. Set once through the app instead.
#
# Three things that look like the answer and are not, all tested against the binary: T3CODE_BASE_DIR
# does not occur in it at all; --base-dir and T3CODE_HOME are T3's own DATA directory, so pointing
# them here would move the SQLite database and four services' OAuth credentials into a directory
# full of checkouts; and T3CODE_PROJECT_ROOT is something T3 SETS for child processes from the
# project's cwd -- declaring it changed nothing, which was confirmed by trying it. Three
# candidates were tested against the binary and all three are wrong: T3CODE_BASE_DIR does not
# appear in it at all; --base-dir and T3CODE_HOME are T3's own DATA directory, so pointing them
# here would move the SQLite database and four services' OAuth credentials into a directory full
# of checkouts; and T3CODE_PROJECT_ROOT is something T3 SETS for child processes from the
# project's cwd, not something it reads. The destination is chosen in the dialog.
#
# The workspace, not the home. \`t3 serve\` takes a working directory as a positional argument and
# otherwise inherits this one, and the add-project flow offers a destination derived from it -- so
# with the home here it proposes cloning into ~/<repo>, which is the directory holding this
# account's OAuth credentials and its SQLite database, at 0700. Checkouts belong in the one place
# the account has a deliberate write grant.
WorkingDirectory=${WORKSPACE}

# The launcher. NOT a path under .t3/runtime/versions -- see the comment above this resource.
# The trailing path is \`cwd\`: the working directory for provider sessions.
ExecStart=${BIN}/t3 serve ${WORKSPACE}

# A system unit inherits neither of these. HOME is set explicitly rather than left for systemd to
# derive from User=, and PATH must contain the provider CLIs because t3 discovers claude, codex and
# opencode by looking for them there -- the default unit PATH does not include this directory, so
# without this the harness starts cleanly and then reports that no providers are installed.
Environment=HOME=${HOME}
# ${HOME}/.local/share/mise/shims comes first because that is how mise exposes what an agent
# installs: a \`go\` or \`node\` it fetched is a shim there, not a binary in ~/.local/bin. Without it
# an agent installs a toolchain successfully and the service still cannot find it, which reads as
# the install having failed.
Environment=PATH=${HOME}/.local/share/mise/shims:${BIN}:/usr/local/bin:/usr/bin:/bin

# Set explicitly rather than left to the default, because it is the thing that silently broke
# the sudo grant once and an absent line does not tell a reader a decision was made.
#
# The name is a double negative: \`no\` means "do NOT forbid new privileges", i.e. sudo works. It
# reads back in /proc/<pid>/status as \`NoNewPrivs: 0\` -- systemd's boolean and the kernel's
# PR_SET_NO_NEW_PRIVS flag are different things describing the same state, which is why the check
# below is on the kernel's value and not on this line.
NoNewPrivileges=no
#
# \`yes\` was right while the account had no sudo, to close the last route to privilege. It is
# incompatible with the sudo grant above: it blocks every setuid binary, and setuid is how sudo
# escalates -- so with it set, sudo refuses before it ever reads /etc/sudoers.d, and an agent sees
# a permission failure that no sudoers rule can fix.
#
# Worth knowing how this hides: \`sudo -u t3code ... sudo apt-get\` over ssh SUCCEEDS, because a
# login shell is not a child of this unit and does not inherit the flag. Only processes the
# service spawns are affected. So the account tests fine and the agents still cannot install
# anything. Verify with NoNewPrivs in /proc/<pid>/status, not with a shell.

Restart=always
RestartSec=5
${unitResourceLines(args.unit)}
StandardOutput=journal
StandardError=journal

[Install]
WantedBy=multi-user.target
`,
    }, { provider, dependsOn: [t3, versions, bin] });
    return {
        account: account_,
        user: account,
        binDirectory: bin,
        home: HOME,
        bin: BIN,
        opt: OPT,
        workspace: WORKSPACE,
        unitName,
        publicKey: key.publicKey,
    };
}
exports.t3codeResource = t3codeResource;
//# sourceMappingURL=index.js.map