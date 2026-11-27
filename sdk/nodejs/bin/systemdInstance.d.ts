import * as pulumi from "@pulumi/pulumi";
/**
 * An instance of a template unit the machine should run.
 */
export declare class SystemdInstance extends pulumi.CustomResource {
    /**
     * Get an existing SystemdInstance resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): SystemdInstance;
    /**
     * Returns true if the given object is an instance of SystemdInstance.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is SystemdInstance;
    readonly enabled: pulumi.Output<boolean>;
    readonly instance: pulumi.Output<string>;
    readonly started: pulumi.Output<boolean>;
    readonly suffix: pulumi.Output<string>;
    readonly template: pulumi.Output<string>;
    /**
     * The unit's full name, as systemd knows it.
     */
    readonly unit: pulumi.Output<string>;
    /**
     * Create a SystemdInstance resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: SystemdInstanceArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a SystemdInstance resource.
 */
export interface SystemdInstanceArgs {
    /**
     * Whether it starts at boot. On by default.
     */
    enabled?: pulumi.Input<boolean>;
    /**
     * The instance, escaped as systemd-escape does.
     */
    instance: pulumi.Input<string>;
    /**
     * Whether it is running now. On by default.
     */
    started?: pulumi.Input<boolean>;
    /**
     * The unit type, service by default.
     */
    suffix?: pulumi.Input<string>;
    /**
     * The template's name, without @ or suffix.
     */
    template: pulumi.Input<string>;
}
