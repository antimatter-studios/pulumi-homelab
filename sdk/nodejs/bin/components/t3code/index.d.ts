import * as pulumi from '@pulumi/pulumi';
import { Directory } from '../../directory';
import { User } from '../../user';
import type { Group } from '../../group';
import type { Provider } from '../../provider';
/** One agent CLI, as a checksummed tarball and the path to its executable inside it. */
export interface ArchiveAgentSpec {
    /** Names the resources — `archive-claude`, `link-claude` — and the command on PATH. */
    key: string;
    /** Where the executable is inside the unpacked tree: 'bin/gh', or 'claude' at the top. */
    bin: string;
    url: string;
    sha256: string;
    /** Leading path components to drop. 0 when the archive has no top-level directory. */
    strip: number;
}
/**
 * One agent CLI, installed by its vendor's own installer and then left to update itself.
 * See `OfficialInstall` for why the coding agents are installed this way and not pinned.
 */
export interface OfficialAgentSpec {
    key: string;
    /** The vendor's install command, run as the account. */
    installer: string;
    /** Where the installer leaves the executable, relative to the account's home. */
    bin: string;
    /**
     * Set when the installer does not put the command in ~/.local/bin itself: a link is made there,
     * to `bin`, so it is on the unit's PATH. The link names a fixed path, and the tool updates the
     * file behind it in place.
     */
    link?: boolean;
}
export type AgentSpec = ArchiveAgentSpec | OfficialAgentSpec;
export interface T3CodeArgs {
    /** The service account. Names every resource, so changing it makes a second install. */
    account?: string;
    /**
     * Its home, and the single most consequential argument here.
     *
     * `~/.t3/userdata` is a SQLite database under continuous write — every thread, every message. On
     * an SD card that is a card with a countdown on it. Put this on real storage.
     */
    home: string;
    /**
     * Where checkouts go, and what the service runs in.
     *
     * Not the home: `t3 serve` takes a working directory, and the add-project flow offers a
     * destination derived from it — so with the home here it proposes cloning into the directory
     * holding this account's OAuth credentials and its database, at 0700.
     *
     * This module does not declare the directory. Whether it is created, adopted, shared or ACL'd is
     * a fact about the machine rather than about T3 Code.
     */
    workspace: string;
    /** Supplementary groups. `kvm` if agents are to use hardware virtualisation. */
    groups?: string[];
    /**
     * The account's uid, for a home carried over from another machine: its files are owned by number.
     * See `User.uid`; an existing account with a different uid is refused, not renumbered.
     */
    uid?: number;
    /** The primary group, declared with its gid, for the same reason. Made before the account. */
    primaryGroup?: Group;
    git: {
        /**
         * The whole of `~/.gitconfig`, as content.
         *
         * Deliberately not assembled from a name and an email here. What goes in it — which credential
         * helpers, which `safe.directory` entries, which forge — is site knowledge, and a generated file
         * that is nearly right is worse than one that is passed in: the resource owns the path, so
         * anything it does not know about gets removed on the next deployment without comment.
         */
        config: string;
        /** The ssh key's comment. Defaults to `<account>@<unit>`. */
        keyComment?: string;
    };
    /** Defaults to gh, claude, codex and opencode at the versions this package was tested against. */
    agents?: AgentSpec[];
    /** The harness itself. Defaults to the pinned version below. */
    t3?: {
        version: string;
        sha256: string;
        url?: (version: string) => string;
    };
    /**
     * The sudoers file. Unrestricted root, and the doc on the resource says why a list is not a
     * boundary.
     */
    sudo?: {
        file?: string;
    } | false;
    unit?: UnitArgs;
    /** Resources the account itself must wait for, such as a supplementary group made elsewhere. */
    accountDependsOn?: pulumi.Resource[];
}
export interface UnitArgs {
    name?: string;
    /**
     * How much of the machine the agents may take. Unset, they may take all of it.
     *
     * Agents run test suites, builds and installs in parallel, and each of those is a child of this
     * unit. On a machine that also serves things around the clock, an uncapped agent workload is the
     * thing that takes those services down: memory runs out, the kernel stalls every process on reclaim,
     * and the cluster's API stops answering long before anything is killed.
     *
     * `memoryHigh` throttles and reclaims; `memoryMax` is where the kernel kills, and it kills inside
     * this unit rather than choosing a victim across the machine. The weights only matter under
     * contention: an idle machine still gives the agents everything.
     */
    limits?: UnitLimits;
    /**
     * `TMPDIR` for everything the harness spawns.
     *
     * Where `/tmp` is a tmpfs, what agents write there is memory that cannot be reclaimed while the
     * file exists, and agents leave a lot behind. A directory on disk turns that into disk usage.
     * This module does not declare the directory, for the same reason it does not declare the
     * workspace: where it lives and how it is aged is a fact about the machine.
     */
    tmpDir?: string;
}
export interface UnitLimits {
    /** systemd `MemoryHigh=`, e.g. `6G`. */
    memoryHigh?: string;
    /** systemd `MemoryMax=`, e.g. `8G`. */
    memoryMax?: string;
    /**
     * systemd `MemorySwapMax=`, e.g. `32G`. Unset, the agents may take all of the machine's swap.
     *
     * `memoryMax` caps RAM only. Over it the kernel does not kill: it pushes the unit's pages out to
     * swap, so on a machine with a lot of swap a runaway process sails past `memoryMax` and grows into
     * all of it. That happened on 2026-10-12: a test binary in an agent's worktree took 89 GB of swap
     * in two hours, the machine ran out of swap and RAM together, and btrfs aborted a transaction for
     * want of memory and forced the array read-only. With this set, the kill happens inside the unit
     * at `memoryMax` + `memorySwapMax`, and the rest of the machine keeps its swap.
     */
    memorySwapMax?: string;
    /**
     * systemd `OOMPolicy=`: what happens to the unit when the kernel kills one of its processes.
     *
     * systemd's default for a service is `stop`, which takes the whole harness and every agent in it
     * down because one test ran out of memory. `continue` lets the kernel kill the one process and
     * leaves the rest running, which is what a memory cap on a unit full of unrelated agents is for.
     */
    oomPolicy?: 'continue' | 'stop' | 'kill';
    /** systemd `CPUWeight=`, 1–10000; the default is 100. */
    cpuWeight?: number;
    /** systemd `IOWeight=`, 1–10000; the default is 100. */
    ioWeight?: number;
}
/**
 * The `[Service]` lines for the limits and `TMPDIR`, or an empty string when neither is set, so a
 * unit that asks for nothing is byte for byte what it was before these options existed.
 */
export declare function unitResourceLines(unit?: UnitArgs): string;
export interface T3Code {
    account: string;
    /**
     * The account itself, so a consumer can order against it.
     *
     * `account` is a plain string and creates no dependency, which matters more here than it looks:
     * a `PosixAcl` naming this user, declared in the stack rather than in this module, is applied by
     * `setfacl` — and `setfacl` for a user that does not exist yet fails rather than waiting.
     */
    user: User;
    /** `~/.local/bin` as a resource, for anything the consumer wants to put in it. */
    binDirectory: Directory;
    home: string;
    /** `~/.local/bin`, for anything the consumer wants to put beside the launchers. */
    bin: string;
    opt: string;
    workspace: string;
    unitName: string;
    /** The public half of the key generated on the machine. The private half never leaves it. */
    publicKey: pulumi.Output<string>;
}
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
export declare const T3_DEFAULT: {
    version: string;
    sha256: string;
    url: (version: string) => string;
};
export declare const AGENT_DEFAULTS: AgentSpec[];
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
export declare function t3codeResource(provider: Provider, args: T3CodeArgs): T3Code;
