import * as pulumi from "@pulumi/pulumi";
/**
 * A remote rclone should know about, read back in plaintext so drift is detectable at all.
 */
export declare class RcloneRemote extends pulumi.CustomResource {
    /**
     * Get an existing RcloneRemote resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): RcloneRemote;
    /**
     * Returns true if the given object is an instance of RcloneRemote.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is RcloneRemote;
    readonly config: pulumi.Output<string>;
    readonly remote: pulumi.Output<string>;
    readonly secrets: pulumi.Output<{
        [key: string]: string;
    }>;
    readonly settings: pulumi.Output<{
        [key: string]: string;
    }>;
    readonly type: pulumi.Output<string>;
    /**
     * Create a RcloneRemote resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: RcloneRemoteArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a RcloneRemote resource.
 */
export interface RcloneRemoteArgs {
    /**
     * Which config file to write. Defaults to root's, /root/.config/rclone/rclone.conf.
     */
    config?: pulumi.Input<string>;
    /**
     * The remote's name, as it appears in [brackets] and before the colon in a path.
     */
    remote: pulumi.Input<string>;
    /**
     * Credentials, given in plaintext and stored obscured. Read back revealed, so the state holds them in plaintext, marked secret.
     */
    secrets?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    }>;
    /**
     * Everything that is not a credential, keyed as rclone spells it.
     */
    settings?: pulumi.Input<{
        [key: string]: pulumi.Input<string>;
    }>;
    /**
     * The backend: sftp, s3, webdav, and so on.
     */
    type: pulumi.Input<string>;
}
