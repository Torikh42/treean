const POW_VERSION = "v1";
const MIN_DIFFICULTY = 1;
const MAX_DIFFICULTY = 24;
const MAX_ITERATIONS = 2_000_000;
const YIELD_INTERVAL = 2048;

interface ParsedChallenge {
  difficulty: number;
}

function parseChallengeToken(challengeToken: string): ParsedChallenge {
  if (
    typeof challengeToken !== "string" ||
    challengeToken.trim() === "" ||
    /\s/.test(challengeToken)
  ) {
    throw new Error("Invalid PoW challenge token format");
  }

  const parts = challengeToken.split(".");
  if (parts.length !== 5 || parts[0] !== POW_VERSION) {
    throw new Error("Invalid PoW challenge token format");
  }

  const [, expiresRaw, difficultyRaw, saltHex, macHex] = parts;
  if (!/^\d+$/.test(expiresRaw)) {
    throw new Error("Invalid PoW challenge expiry");
  }
  if (!/^\d+$/.test(difficultyRaw)) {
    throw new Error("Invalid PoW challenge difficulty");
  }

  const difficulty = Number(difficultyRaw);
  if (!Number.isInteger(difficulty) || difficulty < MIN_DIFFICULTY || difficulty > MAX_DIFFICULTY) {
    throw new Error(
      `Invalid PoW challenge difficulty: must be between ${MIN_DIFFICULTY} and ${MAX_DIFFICULTY}`,
    );
  }

  if (!saltHex || !macHex) {
    throw new Error("Invalid PoW challenge token format");
  }

  return { difficulty };
}

function hasLeadingZeroBits(digest: Uint8Array, bits: number): boolean {
  const fullBytes = Math.floor(bits / 8);
  for (let i = 0; i < fullBytes; i++) {
    if (digest[i] !== 0) {
      return false;
    }
  }

  const remBits = bits % 8;
  if (remBits === 0) {
    return true;
  }

  const mask = (0xff << (8 - remBits)) & 0xff;
  return (digest[fullBytes] & mask) === 0;
}

/**
 * Solves an adaptive Proof-of-Work challenge (`v1.<expiresUnix>.<difficulty>.<saltHex>.<macHex>`)
 * using Web Crypto SHA-256 and returns `"<challengeToken>:<nonce>"`.
 */
export async function solvePowChallenge(challengeToken: string): Promise<string> {
  const subtle = globalThis.crypto?.subtle;
  if (!subtle || typeof subtle.digest !== "function") {
    throw new Error(
      "Web Crypto crypto.subtle is unavailable in this context; HTTPS or a supported browser is required.",
    );
  }

  const { difficulty } = parseChallengeToken(challengeToken);
  const encoder = new TextEncoder();

  for (let nonce = 0; nonce < MAX_ITERATIONS; nonce++) {
    if (nonce > 0 && nonce % YIELD_INTERVAL === 0) {
      await new Promise<void>((resolve) => setTimeout(resolve, 0));
    }

    const candidate = `${challengeToken}:${nonce}`;
    const hashBuffer = await subtle.digest("SHA-256", encoder.encode(candidate));
    if (hasLeadingZeroBits(new Uint8Array(hashBuffer), difficulty)) {
      return candidate;
    }
  }

  throw new Error(`PoW challenge solver exceeded max iterations (${MAX_ITERATIONS})`);
}
