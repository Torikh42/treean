import { defineConfig } from "@hey-api/openapi-ts";

export default defineConfig({
  input: process.env.VITE_OPENAPI_INPUT ?? "./openapi.json",
  output: "src/lib/api/generated",
  plugins: [
    "@hey-api/typescript",
    {
      name: "@hey-api/sdk",
      validator: {
        response: "zod",
      },
    },
    "@hey-api/client-fetch",
    "zod",
    {
      name: "@tanstack/react-query",
      queryOptions: true,
      mutationOptions: true,
    },
  ],
});
