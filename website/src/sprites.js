// Hand-drawn 10×10 pixel sprites for tool icons.
// '.' = transparent, letters = palette keys.

export const SPRITES = {
  cmd: {
    pal: { D: "#3a2413", L: "#f0b264", M: "#d18b43", m: "#9c6227", W: "#fff1d0" },
    rows: [
      "DDDDDDDDDD",
      "DLLLLLLLLD",
      "DLMmMMmMLD",
      "DLMWMMWMLD",
      "DLMmMMmMLD",
      "DLMmMMmMLD",
      "DLMWMMWMLD",
      "DLMmMMmMLD",
      "DLLLLLLLLD",
      "DDDDDDDDDD",
    ],
  },
  exec: {
    // same grid as cmd, recolored purple (repeating command block)
    pal: { D: "#241238", L: "#c9a0ff", M: "#8a4fd0", m: "#5d2f96", W: "#efe4ff" },
    rows: [
      "DDDDDDDDDD",
      "DLLLLLLLLD",
      "DLMmMMmMLD",
      "DLMWMMWMLD",
      "DLMmMMmMLD",
      "DLMmMMmMLD",
      "DLMWMMWMLD",
      "DLMmMMmMLD",
      "DLLLLLLLLD",
      "DDDDDDDDDD",
    ],
  },
  book: {
    pal: { D: "#2b1414", B: "#8a2b2b", P: "#e8dcc0", G: "#caa84b" },
    rows: [
      "..........",
      ".DDDDDDDD.",
      ".DBBBBBBD.",
      ".DBPPPPBD.",
      ".DBPGGPBD.",
      ".DBPGGPBD.",
      ".DBPPPPBD.",
      ".DBBBBBBD.",
      ".DDDDDDDD.",
      "..........",
    ],
  },
  pencil: {
    pal: { R: "#e06a6a", G: "#555555", Y: "#e8c74a", K: "#222222", W: "#d8a26a" },
    rows: [
      "...RRRR...",
      "...GGGG...",
      "...YYYY...",
      "...YYYY...",
      "...YYYY...",
      "...YYYY...",
      "...YYYY...",
      "...WWWW...",
      "....KK....",
      "..........",
    ],
  },
  anvil: {
    pal: { D: "#16161a", M: "#4a4a52", L: "#6a6a74" },
    rows: [
      "..........",
      ".DDDDDDDD.",
      ".DLLLLLLD.",
      ".DDDDDDDD.",
      "...DMMD...",
      "...DMMD...",
      "..DMEMD...",   // placeholder replaced below
      ".DMMMMMD..",
      ".DDDDDDDD.",
      "..........",
    ],
  },
  compass: {
    pal: { D: "#101014", S: "#9a9aa5", F: "#3a3a46", R: "#ff3333", W: "#ffffff" },
    rows: [
      "...DDDD...",
      "..DSSSSD..",
      ".DSFFFFSD.",
      ".DSFRRFSD.",
      ".DSFRWFSD.",
      ".DSFRRFSD.",
      ".DSFFFFSD.",
      "..DSSSSD..",
      "...DDDD...",
      "..........",
    ],
  },
  chest: {
    pal: { D: "#3a2408", C: "#9c6b2f", G: "#e8b93a", c: "#7d5620" },
    rows: [
      "..........",
      ".DDDDDDDD.",
      ".DCCCCCCD.",
      ".DCCCCCCD.",
      "DDDDDDDDDD",
      ".DCCGGCCD.",
      ".DCCGGCCD.",
      ".DccccccD.",
      ".DDDDDDDD.",
      "..........",
    ],
  },
  paper: {
    pal: { W: "#f2f2ee", L: "#9a9a9a", S: "#c9c9c2" },
    rows: [
      "..........",
      ".WWWWWWWW.",
      ".WWWWWWWW.",
      ".WLLLLLLW.",
      ".WWWWWWWW.",
      ".WLLLLLLW.",
      ".WWWWWWWW.",
      ".WLLLLLLW.",
      ".SSSSSSSS.",
      "..........",
    ],
  },
  hammer: {
    pal: { D: "#16161a", M: "#8a8a94", H: "#8a5c28", h: "#6b451c" },
    rows: [
      "..........",
      ".MMMMMMMM.",
      ".MDDDDDDM.",
      ".MMMMMMMM.",
      "....DH....",
      "....DH....",
      "....DH....",
      "....Dh....",
      "....Dh....",
      "..........",
    ],
  },
  lever: {
    pal: { B: "#7a7a7a", b: "#565656", S: "#8a5c28", D: "#16161a" },
    rows: [
      "......SS..",
      ".....SS...",
      "....SS....",
      "...SS.....",
      "..SS......",
      ".DBBBBBBD.",
      ".DBBBBBBD.",
      ".Db bb bbD".replace(/ /g, "b"),
      "..DDDDDD..",
      "..........",
    ],
  },
  pearl: {
    pal: { D: "#14655c", T: "#1fa89a", t: "#0e4a44", H: "#8ff5e0" },
    rows: [
      "..........",
      "...DDDD...",
      "..DTTTTD..",
      ".DTHHTTTD.",
      ".DTTTTTTD.",
      ".DTTTTTTD.",
      "..DttTTD..",
      "...DDDD...",
      "..........",
      "..........",
    ],
  },
  egg: {
    pal: { E: "#8a4fbf", S: "#5d2f85", e: "#a86bd8" },
    rows: [
      "....EE....",
      "...EEEE...",
      "..EEEEEE..",
      ".EESESEEE.",
      ".EEEEEsEE.".replace("s", "S"),
      ".ESEEESEE.",
      ".EEEEESEE.",
      ".EEEEEEEE.",
      "..EEEEEE..",
      "..........",
    ],
  },
  bell: {
    pal: { G: "#f2c94c", g: "#b98a1e", D: "#16161a" },
    rows: [
      "....gg....",
      "...GGGG...",
      "..GGGGGG..",
      "..GGGGGG..",
      ".GGGGGGGG.",
      ".Gggggggg.",
      "DDDDDDDDDD",
      "....DD....",
      "..........",
      "..........",
    ],
  },
  bottle: {
    pal: { C: "#9c6b2f", G: "#b7f7d0", L: "#56d364", l: "#3fa84e" },
    rows: [
      "....CC....",
      "....CC....",
      "....GG....",
      "..GLLLLG..",
      ".GLLLLLLG.",
      ".GLLLLLlG.",
      ".GLLllllG.",
      "..GLLLLG..",
      "...GGGG...",
      "..........",
    ],
  },
  pickaxe: {
    pal: { H: "#b8b8c4", h: "#7a7a88", W: "#8a5c28" },
    rows: [
      ".HHHHHHHH.",
      ".Hh.WW.hH.",
      "....WW....",
      "....WW....",
      "....WW....",
      "....WW....",
      "....WW....",
      "....WW....",
      "....WW....",
      "..........",
    ],
  },
  sign: {
    pal: { P: "#b58b4c", p: "#96703a", D: "#3a2408", W: "#8a5c28" },
    rows: [
      ".PPPPPPPP.",
      ".PPPPPPPP.",
      ".PPpPPpPP.",
      ".PPPPPPPP.",
      "....WW....",
      "....WW....",
      "....WW....",
      "....WW....",
      "....WW....",
      "..........",
    ],
  },
};

// fix anvil row that had a placeholder letter
SPRITES.anvil.rows[6] = "..DMMMD...";

export function drawSprite(canvas, name, scale = 4) {
  const sp = SPRITES[name];
  if (!sp) return;
  const w = sp.rows[0].length, h = sp.rows.length;
  canvas.width = w * scale;
  canvas.height = h * scale;
  const ctx = canvas.getContext("2d");
  ctx.clearRect(0, 0, canvas.width, canvas.height);
  sp.rows.forEach((row, y) => {
    [...row].forEach((ch, x) => {
      if (ch === "." || !sp.pal[ch]) return;
      ctx.fillStyle = sp.pal[ch];
      ctx.fillRect(x * scale, y * scale, scale, scale);
    });
  });
}

export function spriteCanvas(name) {
  const c = document.createElement("canvas");
  c.className = "pixel";
  drawSprite(c, name);
  return c;
}
