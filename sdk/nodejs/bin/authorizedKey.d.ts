import * as pulumi from "@pulumi/pulumi";
/**
 * A key that should be authorised, identified by the key itself rather than by what it is called.
 */
export declare class AuthorizedKey extends pulumi.CustomResource {
    /**
     * Get an existing AuthorizedKey resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): AuthorizedKey;
    /**
     * Returns true if the given object is an instance of AuthorizedKey.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is AuthorizedKey;
    /**
     * The comment on the key as it sits in the file.
     */
    readonly comment: pulumi.Output<string>;
    /**
     * 0700 on ~/.ssh, read back.
     */
    readonly directoryMode: pulumi.Output<string>;
    readonly key: pulumi.Output<string>;
    /**
     * 0600, read back rather than assumed.
     */
    readonly mode: pulumi.Output<string>;
    readonly options: pulumi.Output<string[]>;
    /**
     * Who owns the file, read back.
     */
    readonly owner: pulumi.Output<string>;
    readonly path: pulumi.Output<string>;
    readonly user: pulumi.Output<string>;
    /**
     * Create a AuthorizedKey resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: AuthorizedKeyArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a AuthorizedKey resource.
 */
export interface AuthorizedKeyArgs {
    /**
     * The whole public key line as ssh-keygen prints it.
     */
    key: pulumi.Input<string>;
    /**
     * Restrictions, written before the key: from="10.0.0.0/24", command="…", restrict.
     */
    options?: pulumi.Input<pulumi.Input<string>[]>;
    /**
     * Where the file is, for an account whose home is not where getent says.
     */
    path?: pulumi.Input<string>;
    /**
     * The account whose authorized_keys this is. It must already exist.
     */
    user: pulumi.Input<string>;
}
