// Bun text imports (`with { type: "text" }`) resolve markdown files to their
// contents as a string — both at runtime and embedded into compiled binaries.
declare module "*.md" {
  const contents: string;
  export default contents;
}
