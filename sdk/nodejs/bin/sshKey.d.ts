import * as pulumi from "@pulumi/pulumi";
/**
 * A key pair the machine should have, whose public half can be handed to whatever should trust it.
 */
export declare class SshKey extends pulumi.CustomResource {
    /**
     * Get an existing SshKey resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): SshKey;
    /**
     * Returns true if the given object is an instance of SshKey.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is SshKey;
    readonly allowDelete: pulumi.Output<boolean>;
    readonly comment: pulumi.Output<string>;
    /**
     * SHA256:…, the whole identity of a key pair, safe to print anywhere.
     */
    readonly fingerprint: pulumi.Output<string>;
    readonly group: pulumi.Output<string>;
    readonly owner: pulumi.Output<string>;
    readonly path: pulumi.Output<string>;
    readonly privateMode: pulumi.Output<string>;
    /**
     * The public key line, for handing to whatever should trust it.
     */
    readonly publicKey: pulumi.Output<string>;
    readonly publicMode: pulumi.Output<string>;
    readonly type: pulumi.Output<string>;
    /**
     * Create a SshKey resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: SshKeyArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a SshKey resource.
 */
export interface SshKeyArgs {
    /**
     * Permission to delete the private key when the resource goes. Off, and worth leaving off.
     */
    allowDelete?: pulumi.Input<boolean>;
    /**
     * Bits, for the types that take them. Ignored by ed25519.
     */
    bits?: pulumi.Input<number>;
    /**
     * What goes in the key's comment field. Defaults to the resource's name.
     */
    comment?: pulumi.Input<string>;
    /**
     * The group on both halves. Defaults to root.
     */
    group?: pulumi.Input<string>;
    /**
     * Who owns both halves of the pair. Defaults to root.
     */
    owner?: pulumi.Input<string>;
    /**
     * Where the private key goes; the public key is the same path with .pub.
     */
    path: pulumi.Input<string>;
    /**
     * ed25519 (the default), rsa or ecdsa.
     */
    type?: pulumi.Input<string>;
}
