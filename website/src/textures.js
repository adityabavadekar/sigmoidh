// Procedural 16×16 Minecraft-ish tiles — no assets, just seeded noise.

export function rng(seed) {
  let a = seed >>> 0;
  return function () {
    a |= 0; a = (a + 0x6d2b79f5) | 0;
    let t = Math.imul(a ^ (a >>> 15), 1 | a);
    t = (t + Math.imul(t ^ (t >>> 7), 61 | t)) ^ t;
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

function tile(size, seed, palette, weights) {
  const c = document.createElement("canvas");
  c.width = c.height = size;
  const ctx = c.getContext("2d");
  const r = rng(seed);
  for (let y = 0; y < size; y++) {
    for (let x = 0; x < size; x++) {
      // pick color by weight
      let v = r(), acc = 0, col = palette[palette.length - 1];
      for (let i = 0; i < palette.length; i++) {
        acc += weights[i];
        if (v <= acc) { col = palette[i]; break; }
      }
      ctx.fillStyle = col;
      ctx.fillRect(x, y, 1, 1);
    }
  }
  return c;
}

const P = {
  dirt:   ["#8a6142", "#79553a", "#6a4831", "#5c3d28"],
  grassT: ["#7bb944", "#5d9c3f", "#4f8836", "#68aa3f"],
  stone:  ["#828282", "#747474", "#676767", "#5c5c5c"],
  deep:   ["#3d3d45", "#34343b", "#2b2b31", "#232329"],
  bed:    ["#26262b", "#17171a", "#333338", "#0e0e10"],
  planks: ["#b58b4c", "#a67c40", "#96703a", "#ad8547"],
};

export function makeTextures() {
  const S = 16;
  const t = {
    dirt:     tile(S, 11, P.dirt, [.3, .35, .25, .10]),
    grassTop: tile(S, 22, P.grassT, [.32, .38, .18, .12]),
    stone:    tile(S, 33, P.stone, [.30, .35, .23, .12]),
    deepslate:tile(S, 44, P.deep, [.30, .35, .23, .12]),
    bedrock:  tile(S, 55, P.bed, [.30, .35, .22, .13]),
    planks:   null,
  };

  // planks: horizontal boards with seams
  const pc = document.createElement("canvas");
  pc.width = pc.height = S;
  const px = pc.getContext("2d");
  const pr = rng(66);
  for (let y = 0; y < S; y++) {
    const boardRow = Math.floor(y / 4);
    for (let x = 0; x < S; x++) {
      const seamY = y % 4 === 3;
      const seamX = boardRow % 2 === 0 ? x === 7 : x === 15;
      px.fillStyle = seamY || seamX ? "#6b4d24" : P.planks[Math.floor(pr() * 3)];
      px.fillRect(x, y, 1, 1);
    }
  }
  t.planks = pc;

  // grass side: dirt with green cap + fringe
  const gs = tile(S, 77, P.dirt, [.3, .35, .25, .10]);
  const gx = gs.getContext("2d");
  const gr = rng(88);
  for (let y = 0; y < 4; y++) {
    for (let x = 0; x < S; x++) {
      if (y < 3 || gr() > 0.45) {
        gx.fillStyle = P.grassT[Math.floor(gr() * 3)];
        gx.fillRect(x, y, 1, 1);
      }
    }
  }
  t.grassSide = gs;

  // ores: base + blob clusters
  function ore(seed, colors) {
    const oc = document.createElement("canvas");
    oc.width = oc.height = S;
    const octx = oc.getContext("2d");
    octx.drawImage(t.stone === colors.stoneBase ? t.stone : t.stone, 0, 0); // stone base
    const or = rng(seed);
    const clusters = 3;
    for (let k = 0; k < clusters; k++) {
      const cx = 2 + Math.floor(or() * (S - 4));
      const cy = 2 + Math.floor(or() * (S - 4));
      const offs = [[0,0],[1,0],[0,1],[-1,0],[0,-1],[1,1]];
      for (const [dx, dy] of offs) {
        if (or() > 0.75 && !(dx === 0 && dy === 0)) continue;
        octx.fillStyle = colors[or() > 0.72 ? 1 : 0];
        octx.fillRect(cx + dx, cy + dy, 1, 1);
      }
      octx.fillStyle = colors[colors.length - 1]; // dark shade pixel
      octx.fillRect(cx + 1, cy + 1, 1, 1);
    }
    return oc;
  }

  t.coalOre     = ore(101, ["#2a2a2a", "#161616"]);
  t.ironOre     = ore(102, ["#d8af93", "#af8165"]);
  t.goldOre     = ore(103, ["#fcee4b", "#c8a422"]);
  t.redstoneOre = ore(104, ["#ff2222", "#8f0000"]);
  t.diamondOre  = ore(105, ["#4aedd9", "#21a89a"]);

  return t;
}

// url(canvas) -> css background-image value
export const url = (c) => `url(${c.toDataURL()})`;
