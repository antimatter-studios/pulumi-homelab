import * as pulumi from "@pulumi/pulumi";
/**
 * A service the machine should run, checked against systemd rather than remembered.
 */
export declare class SystemdUnit extends pulumi.CustomResource {
    /**
     * Get an existing SystemdUnit resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): SystemdUnit;
    /**
     * Returns true if the given object is an instance of SystemdUnit.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is SystemdUnit;
    readonly directory: pulumi.Output<string>;
    readonly enabled: pulumi.Output<boolean>;
    readonly mode: pulumi.Output<string>;
    readonly name: pulumi.Output<string>;
    readonly started: pulumi.Output<boolean>;
    readonly unit: pulumi.Output<string>;
    /**
     * What systemctl show says the unit file state is.
     */
    readonly unitFileState: pulumi.Output<string>;
    /**
     * Create a SystemdUnit resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: SystemdUnitArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a SystemdUnit resource.
 */
export interface SystemdUnitArgs {
    /**
     * Where the unit file goes, /etc/systemd/system by default.
     */
    directory?: pulumi.Input<string>;
    /**
     * Whether it starts at boot. On by default.
     */
    enabled?: pulumi.Input<boolean>;
    /**
     * The unit file's mode, 0644 by default.
     */
    mode?: pulumi.Input<string>;
    /**
     * The unit's name, without .service.
     */
    name: pulumi.Input<string>;
    /**
     * Whether it is running now. On by default.
     */
    started?: pulumi.Input<boolean>;
    /**
     * The unit file's content.
     */
    unit: pulumi.Input<string>;
}
