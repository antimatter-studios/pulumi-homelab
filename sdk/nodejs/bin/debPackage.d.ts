import * as pulumi from "@pulumi/pulumi";
/**
 * A .deb fetched from a URL, verified, and installed as a package dpkg knows about.
 */
export declare class DebPackage extends pulumi.CustomResource {
    /**
     * Get an existing DebPackage resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): DebPackage;
    /**
     * Returns true if the given object is an instance of DebPackage.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is DebPackage;
    readonly name: pulumi.Output<string>;
    readonly sha256: pulumi.Output<string>;
    readonly url: pulumi.Output<string>;
    /**
     * What dpkg reports after the install, so a version change is visible in a diff.
     */
    readonly version: pulumi.Output<string>;
    /**
     * Create a DebPackage resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: DebPackageArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a DebPackage resource.
 */
export interface DebPackageArgs {
    /**
     * The name dpkg knows it by, which is not necessarily the filename.
     */
    name: pulumi.Input<string>;
    /**
     * SHA-256 of the file, and not optional: a checksum nobody set is a checksum nobody checked.
     */
    sha256: pulumi.Input<string>;
    /**
     * Where to fetch the .deb from.
     */
    url: pulumi.Input<string>;
}
