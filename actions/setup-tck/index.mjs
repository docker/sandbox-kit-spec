import { install } from './install.mjs';

try {
  const result = await install(process.env);
  console.log(`Installed kit-tck ${result.version} at ${result.path}`);
} catch (error) {
  console.error(`setup-tck: ${error.message}`);
  process.exitCode = 1;
}
