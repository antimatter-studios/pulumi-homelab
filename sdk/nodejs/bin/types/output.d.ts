export declare namespace provider {
    interface Jump {
        address: string;
        hostKeys?: string[];
        port: number;
        user: string;
    }
    interface Source {
        /**
         * For exec and execFile: the program and its arguments, run without a shell. execFile needs a {} somewhere in it.
         */
        argv?: string[];
        /**
         * exec (the material is stdout), execFile (the material is what the command wrote to {}) or file (a file on the deploying machine).
         */
        kind: string;
        /**
         * For file: the path on the deploying machine.
         */
        path?: string;
    }
}
