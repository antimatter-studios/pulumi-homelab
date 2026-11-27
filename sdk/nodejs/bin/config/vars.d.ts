import * as outputs from "../types/output";
/**
 * Address of the managed machine. Required.
 */
export declare const address: string | undefined;
/**
 * Path of the ssh agent socket. Exactly one of agentSocket and agentSocketEnv is required.
 */
export declare const agentSocket: string | undefined;
/**
 * Name of the environment variable holding the agent socket path, e.g. CHORE_AGENT_SOCK.
 */
export declare const agentSocketEnv: string | undefined;
/**
 * How commands become root: "sudo" (sudo -n), "none" (already root) or "sudo-password" (sudo -S with sudoPassword). Required.
 */
export declare const escalation: string | undefined;
/**
 * Pinned host keys of the machine. Exactly one of hostKeys and hostKeysFile is required.
 */
export declare const hostKeys: string[] | undefined;
/**
 * Project file holding the machine's host keys, in known_hosts format.
 */
export declare const hostKeysFile: string | undefined;
/**
 * Optional explicit private key to authenticate with.
 */
export declare const identity: string | undefined;
/**
 * Hops to go through, in order, each with its own address, port, user and pinned host keys.
 */
export declare const jumps: outputs.provider.Jump[] | undefined;
/**
 * ssh port of the managed machine. Required.
 */
export declare const port: number | undefined;
/**
 * Whether the default key files (~/.ssh/id_*) may be read. Required.
 */
export declare const readDefaultKeys: boolean | undefined;
/**
 * Whether ~/.ssh/known_hosts may be read. Required.
 */
export declare const readKnownHosts: boolean | undefined;
/**
 * Whether ~/.ssh/config may be read. Required.
 */
export declare const readSshConfig: boolean | undefined;
/**
 * Whether an unknown or changed host key is refused. Required.
 */
export declare const strictHostKeyChecking: boolean | undefined;
/**
 * The sudo password, required when escalation is "sudo-password" and refused otherwise.
 */
export declare const sudoPassword: string | undefined;
/**
 * User to connect as. Required.
 */
export declare const user: string | undefined;
