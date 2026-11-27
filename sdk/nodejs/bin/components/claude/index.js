"use strict";
Object.defineProperty(exports, "__esModule", { value: true });
exports.claudeProfileResource = exports.claudeSharedResource = exports.profileRefusal = exports.sharedTarget = exports.SHARED_DEFAULTS = void 0;
const directory_1 = require("../../directory");
const symlink_1 = require("../../symlink");
/**
 * What is shared by default, and it is smaller than it looks like it should be.
 *
 * Taken from a working setup rather than from what seemed reasonable. A hand-written script that
 * preceded this listed `skills`, `hooks` and `workgroups` as well — and linked none of them, because
 * it only linked entries that already existed on the shared side and those three never did. A
 * default that creates directories the program does not use is a default that has to be explained
 * later.
 */
exports.SHARED_DEFAULTS = [
    { name: 'projects', kind: 'directory', mode: '0700' },
    { name: 'sessions', kind: 'directory', mode: '0700' },
    { name: 'session-env', kind: 'directory', mode: '0775' },
    { name: 'plugins', kind: 'directory', mode: '0775' },
    { name: 'settings.json', kind: 'file' },
];
/**
 * The link's target, spelled absolutely.
 *
 * Relative would be shorter and is wrong here: these directories are read by a program that resolves
 * paths from wherever it happens to be running, and one of them — `plugins` — is walked by tooling
 * that follows the link and reports what it found. An absolute target says the same thing from
 * every working directory.
 */
function sharedTarget(shared, entry) {
    return `${shared.replace(/\/+$/, '')}/${entry}`;
}
exports.sharedTarget = sharedTarget;
/**
 * The refusal for a profile that would link a directory to itself.
 *
 * A profile whose `dir` is the backend produces, for each entry, a symlink whose target is the path
 * the symlink is at. The loop that creates them succeeds. What follows is a program that opens
 * `projects`, follows a link to `projects`, and gets ELOOP — reported as a corrupt installation
 * rather than as a description that asked for something impossible.
 */
function profileRefusal(args) {
    const shared = args.shared.path.replace(/\/+$/, '');
    const dir = args.dir.replace(/\/+$/, '');
    if (shared === dir)
        return `claudeProfileResource: dir and shared are the same directory (${dir}); a profile has to be somewhere else`;
    return undefined;
}
exports.profileRefusal = profileRefusal;
/**
 * The backend every profile links into, declared once.
 *
 * Separate from `claudeProfileResource` for one reason, and it is the failure this package keeps meeting:
 * two profiles each declaring `~/.claude` would be two resources owning one path, re-applying
 * different answers on alternate runs with both reporting success. The backend has one owner and
 * the profiles depend on it.
 *
 * It is also the default profile. Nothing here is specific to being *shared* — a machine with one
 * login has exactly this and no `claudeProfileResource` at all.
 */
function claudeSharedResource(provider, name, args, opts) {
    var _a, _b, _c;
    const entries = (_a = args.entries) !== null && _a !== void 0 ? _a : exports.SHARED_DEFAULTS;
    const group = (_b = args.group) !== null && _b !== void 0 ? _b : args.account;
    const root = new directory_1.Directory(name, {
        path: args.path, owner: args.account, group, mode: (_c = args.mode) !== null && _c !== void 0 ? _c : '0775',
    }, Object.assign(Object.assign({}, opts), { provider }));
    /**
     * The entry directories, declared so a link cannot dangle.
     *
     * A symlink to a directory that does not exist is created happily and fails on first use, and the
     * failure names the profile rather than the missing target. Claude makes these itself on first
     * run — but only for whichever profile runs first, and every other one is broken until then.
     *
     * A `file` entry is not created: an empty `settings.json` is not the same as no `settings.json`,
     * and writing one would be this module having an opinion about the program's defaults.
     */
    const made = entries
        .filter((entry) => entry.kind === 'directory')
        .map((entry) => {
        var _a;
        return new directory_1.Directory(`${name}-${entry.name}`, {
            path: sharedTarget(args.path, entry.name),
            owner: args.account,
            group,
            mode: (_a = entry.mode) !== null && _a !== void 0 ? _a : '0700',
        }, Object.assign(Object.assign({}, opts), { provider, dependsOn: [root] }));
    });
    return { path: args.path, entries, resources: [root, ...made] };
}
exports.claudeSharedResource = claudeSharedResource;
/** A Claude Code profile: its own credentials, the shared account's work. */
function claudeProfileResource(provider, name, args, opts) {
    var _a, _b;
    const refusal = profileRefusal(args);
    if (refusal)
        throw new Error(refusal);
    const group = (_a = args.group) !== null && _a !== void 0 ? _a : args.account;
    const dir = new directory_1.Directory(name, {
        path: args.dir, owner: args.account, group, mode: (_b = args.mode) !== null && _b !== void 0 ? _b : '0775',
    }, Object.assign(Object.assign({}, opts), { provider }));
    const links = args.shared.entries.map((entry) => new symlink_1.Symlink(`${name}-${entry.name}`, {
        path: sharedTarget(args.dir, entry.name),
        target: sharedTarget(args.shared.path, entry.name),
    }, Object.assign(Object.assign({}, opts), { provider, dependsOn: [dir, ...args.shared.resources] })));
    return { dir: args.dir, configDir: args.dir, links };
}
exports.claudeProfileResource = claudeProfileResource;
//# sourceMappingURL=index.js.map