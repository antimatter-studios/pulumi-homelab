import * as pulumi from "@pulumi/pulumi";
/**
 * One fstab entry, edited in place without disturbing the rest of the file.
 */
export declare class FstabEntry extends pulumi.CustomResource {
    /**
     * Get an existing FstabEntry resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): FstabEntry;
    /**
     * Returns true if the given object is an instance of FstabEntry.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is FstabEntry;
    readonly dump: pulumi.Output<number>;
    readonly file: pulumi.Output<string>;
    /**
     * The entry as the file has it.
     */
    readonly line: pulumi.Output<string>;
    readonly mount: pulumi.Output<boolean>;
    /**
     * What findmnt says is mounted there, if anything.
     */
    readonly mounted: pulumi.Output<string | undefined>;
    readonly options: pulumi.Output<string[]>;
    readonly pass: pulumi.Output<number>;
    readonly source: pulumi.Output<string>;
    readonly target: pulumi.Output<string>;
    readonly type: pulumi.Output<string>;
    /**
     * Create a FstabEntry resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: FstabEntryArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a FstabEntry resource.
 */
export interface FstabEntryArgs {
    /**
     * The dump field, 0 by default.
     */
    dump?: pulumi.Input<number>;
    /**
     * The fstab, /etc/fstab by default.
     */
    file?: pulumi.Input<string>;
    /**
     * Whether to mount it now if it is not mounted. Off by default.
     */
    mount?: pulumi.Input<boolean>;
    /**
     * Mount options; ['defaults'] by default.
     */
    options?: pulumi.Input<pulumi.Input<string>[]>;
    /**
     * The fsck pass, 0 by default.
     */
    pass?: pulumi.Input<number>;
    /**
     * What is mounted: a device, UUID=…, LABEL=… or a remote share.
     */
    source: pulumi.Input<string>;
    /**
     * The mount point, which identifies the entry.
     */
    target: pulumi.Input<string>;
    /**
     * The filesystem type.
     */
    type: pulumi.Input<string>;
}
