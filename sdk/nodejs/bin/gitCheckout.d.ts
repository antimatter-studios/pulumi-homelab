import * as pulumi from "@pulumi/pulumi";
/**
 * A repository at a pinned commit, read back with `git rev-parse HEAD`. Deleting it is refused while it holds work that exists nowhere else.
 */
export declare class GitCheckout extends pulumi.CustomResource {
    /**
     * Get an existing GitCheckout resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): GitCheckout;
    /**
     * Returns true if the given object is an instance of GitCheckout.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is GitCheckout;
    readonly commit: pulumi.Output<string>;
    /**
     * What the checkout is at now: the read, and where a hand `git checkout` shows up.
     */
    readonly head: pulumi.Output<string>;
    readonly path: pulumi.Output<string>;
    readonly url: pulumi.Output<string>;
    /**
     * Create a GitCheckout resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: GitCheckoutArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a GitCheckout resource.
 */
export interface GitCheckoutArgs {
    /**
     * The commit to check out, as a full or abbreviated sha. A tag or a branch name is refused rather than resolved: get the sha with `git ls-remote <url> <tag>` and keep the tag beside it in a comment.
     */
    commit: pulumi.Input<string>;
    /**
     * Where the working tree goes. This is the resource's identity.
     */
    path: pulumi.Input<string>;
    /**
     * Where to clone from.
     */
    url: pulumi.Input<string>;
}
