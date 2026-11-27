import * as pulumi from "@pulumi/pulumi";
/**
 * Software from a verified archive, installed once and thereafter left to manage itself.
 */
export declare class Archive extends pulumi.CustomResource {
    /**
     * Get an existing Archive resource's state with the given name, ID, and optional extra
     * properties used to qualify the lookup.
     *
     * @param name The _unique_ name of the resulting resource.
     * @param id The _unique_ provider ID of the resource to lookup.
     * @param opts Optional settings to control the behavior of the CustomResource.
     */
    static get(name: string, id: pulumi.Input<pulumi.ID>, opts?: pulumi.CustomResourceOptions): Archive;
    /**
     * Returns true if the given object is an instance of Archive.  This is designed to work even
     * when multiple copies of the Pulumi SDK have been loaded into the same process.
     */
    static isInstance(obj: any): obj is Archive;
    readonly binary: pulumi.Output<string | undefined>;
    readonly format: pulumi.Output<string | undefined>;
    readonly group: pulumi.Output<string>;
    readonly healthCommand: pulumi.Output<string | undefined>;
    /**
     * Whether it is there: the read, and the only thing this resource acts on.
     */
    readonly installed: pulumi.Output<boolean>;
    readonly link: pulumi.Output<string | undefined>;
    readonly name: pulumi.Output<string>;
    /**
     * Who owns the prefix itself, as the machine reports it after a read and as declared after an apply.
     */
    readonly owner: pulumi.Output<string>;
    readonly prefix: pulumi.Output<string>;
    readonly sha256: pulumi.Output<string | undefined>;
    readonly strip: pulumi.Output<number | undefined>;
    readonly url: pulumi.Output<string | undefined>;
    /**
     * What the software says it is, or the empty string when it could not be asked. Nothing compares it.
     */
    readonly version: pulumi.Output<string>;
    readonly versionCommand: pulumi.Output<string>;
    readonly versionPattern: pulumi.Output<string | undefined>;
    /**
     * Create a Archive resource with the given unique name, arguments, and options.
     *
     * @param name The _unique_ name of the resource.
     * @param args The arguments to use to populate this resource's properties.
     * @param opts A bag of options that control this resource's behavior.
     */
    constructor(name: string, args: ArchiveArgs, opts?: pulumi.CustomResourceOptions);
}
/**
 * The set of arguments for constructing a Archive resource.
 */
export interface ArchiveArgs {
    /**
     * Something under the install that has to exist and be executable for it to count as installed. A relative path is taken from link when there is one and prefix otherwise.
     */
    binary?: pulumi.Input<string>;
    /**
     * tar.gz, tar.xz, tar.bz2 or zip. Inferred from the url when not given.
     */
    format?: pulumi.Input<string>;
    /**
     * The group on the unpacked tree. Defaults to root.
     */
    group?: pulumi.Input<string>;
    /**
     * Something that exits zero when the install works, whose output is discarded. {} expands to the link if there is one and the prefix otherwise.
     */
    healthCommand?: pulumi.Input<string>;
    /**
     * A stable path symlinked at prefix, so everything else refers to one place across upgrades.
     */
    link?: pulumi.Input<string>;
    /**
     * What it is, for the id and for messages.
     */
    name: pulumi.Input<string>;
    /**
     * Who owns the unpacked tree. Defaults to root.
     */
    owner?: pulumi.Input<string>;
    /**
     * Where it unpacks.
     */
    prefix: pulumi.Input<string>;
    /**
     * SHA-256 of the archive, and not optional. A checksum nobody set is a checksum nobody checked.
     */
    sha256: pulumi.Input<string>;
    /**
     * Leading path components to strip.
     */
    strip?: pulumi.Input<number>;
    /**
     * Where the archive is.
     */
    url: pulumi.Input<string>;
    /**
     * How to ask the software what version it is, purely for display. Reported, never compared. {} expands as for healthCommand.
     */
    versionCommand?: pulumi.Input<string>;
    /**
     * A JavaScript regular expression with one capture group, matched against what versionCommand prints. The pattern must compile under RE2.
     */
    versionPattern?: pulumi.Input<string>;
}
