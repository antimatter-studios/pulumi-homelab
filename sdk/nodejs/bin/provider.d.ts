import * as pulumi from "@pulumi/pulumi";
import * as inputs from "./types/input";
export declare class Provider extends pulumi.ProviderResource {
    /**
     * Returns true if the given object is an instance of Provider.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is Provider;
    /**
     * Address of the managed machine. Required.
     */
    readonly address: pulumi.Output<string | undefined>;
    /**
     * Path of the ssh agent socket. Exactly one of agentSocket and agentSocketEnv is required.
     */
    readonly agentSocket: pulumi.Output<string | undefined>;
    /**
     * Name of the environment variable holding the agent socket path, e.g. CHORE_AGENT_SOCK.
     */
    readonly agentSocketEnv: pulumi.Output<string | undefined>;
    /**
     * How commands become root: "sudo" (sudo -n), "none" (already root) or "sudo-password" (sudo -S with sudoPassword). Required.
     */
    readonly escalation: pulumi.Output<string | undefined>;
    /**
     * Project file holding the machine's host keys, in known_hosts format.
     */
    readonly hostKeysFile: pulumi.Output<string | undefined>;
    /**
     * Optional explicit private key to authenticate with.
     */
    readonly identity: pulumi.Output<string | undefined>;
    /**
     * The sudo password, required when escalation is "sudo-password" and refused otherwise.
     */
    readonly sudoPassword: pulumi.Output<string | undefined>;
    /**
     * User to connect as. Required.
     */
    readonly user: pulumi.Output<string | undefined>;
    /**
     * Create a Provider resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args?: ProviderArgs, opts?: pulumi.ResourceOptions);
}
/**
 * The set of arguments for constructing a Provider resource.
 */
export interface ProviderArgs {
    /**
     * Address of the managed machine. Required.
     */
    address?: pulumi.Input<string>;
    /**
     * Path of the ssh agent socket. Exactly one of agentSocket and agentSocketEnv is required.
     */
    agentSocket?: pulumi.Input<string>;
    /**
     * Name of the environment variable holding the agent socket path, e.g. CHORE_AGENT_SOCK.
     */
    agentSocketEnv?: pulumi.Input<string>;
    /**
     * How commands become root: "sudo" (sudo -n), "none" (already root) or "sudo-password" (sudo -S with sudoPassword). Required.
     */
    escalation?: pulumi.Input<string>;
    /**
     * Pinned host keys of the machine. Exactly one of hostKeys and hostKeysFile is required.
     */
    hostKeys?: pulumi.Input<pulumi.Input<string>[]>;
    /**
     * Project file holding the machine's host keys, in known_hosts format.
     */
    hostKeysFile?: pulumi.Input<string>;
    /**
     * Optional explicit private key to authenticate with.
     */
    identity?: pulumi.Input<string>;
    /**
     * Hops to go through, in order, each with its own address, port, user and pinned host keys.
     */
    jumps?: pulumi.Input<pulumi.Input<inputs.provider.JumpArgs>[]>;
    /**
     * ssh port of the managed machine. Required.
     */
    port?: pulumi.Input<number>;
    /**
     * Whether the default key files (~/.ssh/id_*) may be read. Required.
     */
    readDefaultKeys?: pulumi.Input<boolean>;
    /**
     * Whether ~/.ssh/known_hosts may be read. Required.
     */
    readKnownHosts?: pulumi.Input<boolean>;
    /**
     * Whether ~/.ssh/config may be read. Required.
     */
    readSshConfig?: pulumi.Input<boolean>;
    /**
     * Whether an unknown or changed host key is refused. Required.
     */
    strictHostKeyChecking?: pulumi.Input<boolean>;
    /**
     * The sudo password, required when escalation is "sudo-password" and refused otherwise.
     */
    sudoPassword?: pulumi.Input<string>;
    /**
     * User to connect as. Required.
     */
    user?: pulumi.Input<string>;
}
