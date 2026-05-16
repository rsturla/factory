// Simple TypeScript pipeline without SDK dependency (for testing Bun integration)

const spec = {
  name: "simple-ts-test",
  resources: {
    "test-repo": {
      type: "git",
      access: "read-only"
    }
  },
  stages: [
    {
      name: "test",
      agent: {
        image: "busybox",
        command: ["sh", "-c", "echo hello > /output/result.txt"],
        resources: ["test-repo"]
      },
      output: {
        type: "report"
      }
    }
  ]
};

console.log(JSON.stringify(spec));
