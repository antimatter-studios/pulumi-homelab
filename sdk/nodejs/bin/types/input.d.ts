import * as pulumi from "@pulumi/pulumi";
export declare namespace provider {
    interface JumpArgs {
        address: pulumi.Input<string>;
        hostKeys?: pulumi.Input<pulumi.Input<string>[]>;
        port: pulumi.Input<number>;
        user: pulumi.Input<string>;
    }
    interface SourceArgs {
        /**
         * For exec and execFile: the program and its arguments, run without a shell. execFile needs a {} somewhere in it.
         */
        argv?: pulumi.Input<pulumi.Input<string>[]>;
        /**
         * exec (the material is stdout), execFile (the material is what the command wrote to {}) or file (a file on the deploying machine).
         */
        kind: pulumi.Input<string>;
        /**
         * For file: the path on the deploying machine.
         */
        path?: pulumi.Input<string>;
    }
}
