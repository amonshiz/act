const fs = require('fs');

// act passes inputs as INPUT_<NAME> with hyphens kept.
const who = process.env['INPUT_WHO-TO-GREET'];
if (!who) {
  console.error('INPUT_WHO-TO-GREET is not set');
  process.exit(1);
}
// realpath: in act's own tests the guest mount is a symlink.
if (fs.realpathSync(process.cwd()) !== fs.realpathSync(process.env.GITHUB_WORKSPACE)) {
  console.error(`cwd ${process.cwd()} is not the workspace ${process.env.GITHUB_WORKSPACE}`);
  process.exit(1);
}
fs.appendFileSync(process.env.GITHUB_OUTPUT, `greeting=Hello ${who}\n`);
console.log(`Hello ${who}`);
