// semantic-release derives the next version and the release notes from the
// commit history, which is what makes Conventional Commits load-bearing here
// rather than cosmetic: a non-conforming subject silently produces no release.
module.exports = {
  branches: ["main"],
  tagFormat: "v${version}",
  repositoryUrl: "https://github.com/oleg-tkachuk/hetzner-iac.git",
  plugins: [
    ["@semantic-release/commit-analyzer", { preset: "conventionalcommits" }],
    "@semantic-release/release-notes-generator",
    // No changelog commit and no npm publish: this repository ships
    // infrastructure, not a package, so the GitHub release IS the artifact.
    ["@semantic-release/github", { successComment: false, failComment: false }],
  ],
};
