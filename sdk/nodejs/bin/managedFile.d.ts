import * as pulumi from "@pulumi/pulumi";
/**
 * A file that is kept as the code says it should be, and noticed when it is not.
 */
export declare class ManagedFile extends pulumi.CustomResource {
    /**
     * Get an existing ManagedFile resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): ManagedFile;
    /**
     * Returns true if the given object is an instance of ManagedFile.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is ManagedFile;
    /**
     * SHA-256 in hex of the declared content after a write or the observed content after a read.
     */
    readonly digest: pulumi.Output<string>;
    readonly group: pulumi.Output<string>;
    readonly mode: pulumi.Output<string>;
    readonly owner: pulumi.Output<string>;
    readonly path: pulumi.Output<string>;
    readonly reloadSystemd: pulumi.Output<boolean | undefined>;
    /**
     * Create a ManagedFile resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: ManagedFileArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a ManagedFile resource.
 */
export interface ManagedFileArgs {
    /**
     * The file's content. A missing final newline is added, as a here-document would.
     */
    content: pulumi.Input<string>;
    /**
     * Owning group.
     */
    group?: pulumi.Input<string>;
    /**
     * Octal, as it is written everywhere else: '0644'.
     */
    mode?: pulumi.Input<string>;
    /**
     * Owning user.
     */
    owner?: pulumi.Input<string>;
    /**
     * Absolute path of the file on the machine. A new path is a new file.
     */
    path: pulumi.Input<string>;
    /**
     * Run `systemctl daemon-reload` after writing: for a static unit, one SystemdUnit cannot describe.
     */
    reloadSystemd?: pulumi.Input<boolean>;
}
