import * as pulumi from "@pulumi/pulumi";
/**
 * A directory the machine should have, checked against stat rather than remembered.
 */
export declare class Directory extends pulumi.CustomResource {
    /**
     * Get an existing Directory resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): Directory;
    /**
     * Returns true if the given object is an instance of Directory.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is Directory;
    readonly group: pulumi.Output<string>;
    /**
     * The four-digit mode, special bits and all: the shape stat answers in.
     */
    readonly mode: pulumi.Output<string>;
    readonly owner: pulumi.Output<string>;
    readonly path: pulumi.Output<string>;
    readonly setgid: pulumi.Output<boolean | undefined>;
    readonly sticky: pulumi.Output<boolean | undefined>;
    /**
     * Create a Directory resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: DirectoryArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a Directory resource.
 */
export interface DirectoryArgs {
    /**
     * The owning group.
     */
    group?: pulumi.Input<string>;
    /**
     * Octal, as it is written everywhere else: '0755'. Four digits are accepted and mean what they always have; a non-zero leading digit beside setgid or sticky is refused, because that is two answers to one question.
     */
    mode?: pulumi.Input<string>;
    /**
     * The owning user.
     */
    owner?: pulumi.Input<string>;
    /**
     * The directory. Its parents are made as needed and never removed.
     */
    path: pulumi.Input<string>;
    /**
     * New entries inherit this directory's group rather than their creator's: what makes a shared area shared.
     */
    setgid?: pulumi.Input<boolean>;
    /**
     * Only an entry's owner may remove it. The bit that belongs beside any write grant on a shared directory.
     */
    sticky?: pulumi.Input<boolean>;
}
