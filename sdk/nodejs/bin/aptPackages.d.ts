import * as pulumi from "@pulumi/pulumi";
/**
 * The list of packages a machine should have, checked against dpkg rather than remembered.
 */
export declare class AptPackages extends pulumi.CustomResource {
    /**
     * Get an existing AptPackages resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): AptPackages;
    /**
     * Returns true if the given object is an instance of AptPackages.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is AptPackages;
    readonly absent: pulumi.Output<string[] | undefined>;
    /**
     * Names declared present that apt holds as auto rather than manual, which an autoremove is entitled to take.
     */
    readonly auto: pulumi.Output<string[]>;
    /**
     * What dpkg says is installed, name to version, for the names asked about.
     */
    readonly installed: pulumi.Output<{
        [key: string]: string;
    }>;
    /**
     * Names declared absent that dpkg still has. Empty on a machine that matches the code.
     */
    readonly lingering: pulumi.Output<string[]>;
    /**
     * Names declared present that dpkg does not have. Empty on a machine that matches the code.
     */
    readonly missing: pulumi.Output<string[]>;
    readonly names: pulumi.Output<string[] | undefined>;
    readonly present: pulumi.Output<string[] | undefined>;
    readonly update: pulumi.Output<boolean | undefined>;
    /**
     * Create a AptPackages resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args?: AptPackagesArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a AptPackages resource.
 */
export interface AptPackagesArgs {
    /**
     * Packages that should not be installed. Purged only when apt would take nothing else with them.
     */
    absent?: pulumi.Input<pulumi.Input<string>[]>;
    /**
     * Accepted as a synonym for present, which is what it was called first.
     */
    names?: pulumi.Input<pulumi.Input<string>[]>;
    /**
     * Packages that should be installed.
     */
    present?: pulumi.Input<pulumi.Input<string>[]>;
    /**
     * Whether to refresh the package lists first.
     */
    update?: pulumi.Input<boolean>;
}
