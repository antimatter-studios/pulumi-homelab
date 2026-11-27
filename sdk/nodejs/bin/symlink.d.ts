import * as pulumi from "@pulumi/pulumi";
/**
 * A symlink that is checked for what it actually is, not merely whether the path exists.
 */
export declare class Symlink extends pulumi.CustomResource {
    /**
     * Get an existing Symlink resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): Symlink;
    /**
     * Returns true if the given object is an instance of Symlink.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is Symlink;
    readonly acceptLauncherScript: pulumi.Output<boolean | undefined>;
    readonly path: pulumi.Output<string>;
    readonly target: pulumi.Output<string>;
    /**
     * Create a Symlink resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: SymlinkArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a Symlink resource.
 */
export interface SymlinkArgs {
    /**
     * Accept a launcher script in place of the link: a small `#!/bin/sh` file whose job is to `exec '<program>'`. Its program is read back as the target. For software that updates itself by rewriting its own launcher. Only a script that small and that shape is accepted; anything else is still refused as data.
     */
    acceptLauncherScript?: pulumi.Input<boolean>;
    /**
     * Where the link itself lives.
     */
    path: pulumi.Input<string>;
    /**
     * What it points at. Not required to exist: a link to a mount point that is not up yet is normal.
     */
    target: pulumi.Input<string>;
}
