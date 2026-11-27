import * as pulumi from "@pulumi/pulumi";
export declare namespace provider {
    interface JumpArgs {
        address: pulumi.Input<string>;
        hostKeys?: pulumi.Input<pulumi.Input<string>[]>;
        port: pulumi.Input<number>;
        user: pulumi.Input<string>;
    }
}
