import * as pulumi from "@pulumi/pulumi";
/**
 * A swap partition found by its GPT name: created in free space only, formatted only when blank, swapping at a priority and enabled at boot by a systemd swap unit. Delete keeps the partition unless told otherwise.
 */
export declare class SwapPartition extends pulumi.CustomResource {
    /**
     * Get an existing SwapPartition resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): SwapPartition;
    /**
     * Returns true if the given object is an instance of SwapPartition.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is SwapPartition;
    /**
     * Whether the kernel is swapping to it now.
     */
    readonly active: pulumi.Output<boolean>;
    /**
     * The priority the kernel is using it at.
     */
    readonly activePriority: pulumi.Output<number | undefined>;
    readonly deletePartition: pulumi.Output<boolean>;
    /**
     * The partition's device node.
     */
    readonly device: pulumi.Output<string>;
    readonly discard: pulumi.Output<string | undefined>;
    readonly disk: pulumi.Output<string>;
    /**
     * The disk the partition is on, as the kernel names it.
     */
    readonly diskDevice: pulumi.Output<string>;
    /**
     * The swap signature's label.
     */
    readonly label: pulumi.Output<string>;
    readonly name: pulumi.Output<string>;
    /**
     * The partition number.
     */
    readonly number: pulumi.Output<number>;
    /**
     * The partition's GPT type GUID.
     */
    readonly partType: pulumi.Output<string>;
    /**
     * The partition's GPT unique id.
     */
    readonly partUuid: pulumi.Output<string>;
    readonly persistent: pulumi.Output<boolean>;
    readonly priority: pulumi.Output<number>;
    /**
     * What blkid finds on the partition: swap, empty for nothing, or the other thing it holds.
     */
    readonly signature: pulumi.Output<string>;
    /**
     * The partition's size in bytes.
     */
    readonly sizeBytes: pulumi.Output<number>;
    readonly sizeGib: pulumi.Output<number>;
    readonly start: pulumi.Output<number | undefined>;
    /**
     * The partition's first sector.
     */
    readonly startSector: pulumi.Output<number>;
    /**
     * The systemd swap unit's name.
     */
    readonly unit: pulumi.Output<string>;
    /**
     * The swap unit file as the machine has it, absent when there is none.
     */
    readonly unitFile: pulumi.Output<string | undefined>;
    /**
     * Create a SwapPartition resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: SwapPartitionArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a SwapPartition resource.
 */
export interface SwapPartitionArgs {
    /**
     * Whether delete also removes the partition from the partition table. Off by default: delete turns the swap off and removes the unit, and the partition and its data stay.
     */
    deletePartition?: pulumi.Input<boolean>;
    /**
     * once, pages or both: what swapon's --discard does. Applied when the swap is next switched on (a reboot, or a priority change); the kernel does not report it, so it is compared with what was declared.
     */
    discard?: pulumi.Input<string>;
    /**
     * The disk the partition is made on, e.g. /dev/nvme0n1. Only used to create it: the partition is found by its name wherever it is, so disks enumerated in another order are not a change.
     */
    disk: pulumi.Input<string>;
    /**
     * The GPT partition name, which is also the swap label and the id (import with the name, or <disk>:<name>). 1-16 characters of A-Z, a-z, 0-9, '.', '_' and '-'.
     */
    name: pulumi.Input<string>;
    /**
     * Whether the swap comes back at boot, through a systemd swap unit this resource writes. On by default. Off writes no unit (and removes this resource's own) while the swap stays on now: for adopting a partition something else still enables at boot.
     */
    persistent?: pulumi.Input<boolean>;
    /**
     * The swap priority, 0-32767. Higher is used first.
     */
    priority: pulumi.Input<number>;
    /**
     * The partition's size in GiB. A different size replaces the resource; an existing partition is never resized.
     */
    sizeGib: pulumi.Input<number>;
    /**
     * The first sector of a new partition. By default the first 1 MiB boundary after the last partition. Either way only free space is used.
     */
    start?: pulumi.Input<number>;
}
