import * as pulumi from "@pulumi/pulumi";
/**
 * A package the machine should have, checked against dpkg rather than remembered.
 */
export declare class AptPackage extends pulumi.CustomResource {
    /**
     * Get an existing AptPackage resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): AptPackage;
    /**
     * Returns true if the given object is an instance of AptPackage.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is AptPackage;
    /**
     * Whether apt holds it as manually installed rather than as somebody else's dependency.
     */
    readonly manual: pulumi.Output<boolean>;
    readonly name: pulumi.Output<string>;
    readonly update: pulumi.Output<boolean>;
    /**
     * What is actually installed, which is how a version bump shows up in a diff without being asked for.
     */
    readonly version: pulumi.Output<string>;
    /**
     * Create a AptPackage resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: AptPackageArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a AptPackage resource.
 */
export interface AptPackageArgs {
    /**
     * What apt calls it.
     */
    name: pulumi.Input<string>;
    /**
     * Whether to refresh the package lists before installing. Off by default because it is slow and shared.
     */
    update?: pulumi.Input<boolean>;
}
