"""Original synthetic NBT fixtures (repository MIT license), NOT game saves.
Independent bit-string writer: no decoder code is imported. Geometry is literal.
Run python3 internal/mapdata/testdata/generate.py to reproduce.
"""
from pathlib import Path
import struct
p=Path(__file__).parent

def text(s):
 b=s.encode(); return struct.pack('>H',len(b))+b

def tag(k,name,payload): return bytes([k])+text(name)+payload

def comp(fields): return b''.join(fields)+b'\0'

def integer(name,x): return tag(3,name,struct.pack('>i',x))

def array(name,b): return tag(7,name,struct.pack('>i',len(b))+b)

def string(name,s): return tag(8,name,text(s))

def section(version,sy):
 fields=[tag(1,'Y',struct.pack('b',sy))]
 # Literal indices correspond to (x,y,z): (0,0,0), (0,5,0),
 # (12,0,0), (13,0,0), (15,15,15). x changes fastest, then z, then y.
 values={0:1,1280:2,12:16,13:3,4095:4}
 if version==1343:
  ids={0:1,1280:2,12:0x301,13:0x302,4095:8}
  blocks=bytearray(4096);data=bytearray(2048);add=bytearray(2048)
  for i,v in ids.items():
   blocks[i]=v&255;add[i//2]|=(v>>8)<<((i%2)*4)
  data[6]=7;data[6]|=5<<4
  return comp(fields+[array('Blocks',blocks),array('Data',data),array('Add',add)])
 names=['minecraft:air','minecraft:stone','minecraft:grass_block','example:ore','minecraft:water']+[f'example:filler{i}' for i in range(5,16)]+['example:boundary']
 palette=bytes([10])+struct.pack('>i',len(names))+b''.join(comp([string('Name',n)]) for n in names)
 # Store each 5-bit value as a string, then split into 64-bit words. Padded
 # storage uses twelve values and four unused bits in each word.
 vals=[values.get(i,0) for i in range(4096)]
 groups=[vals] if version<2529 else [vals[i:i+12] for i in range(0,4096,12)]
 words=[]
 for group in groups:
  bits=''.join(f'{v:05b}'[::-1] for v in group)
  for i in range(0,len(bits),64):words.append(int(bits[i:i+64].ljust(64,'0')[::-1],2))
 packed=struct.pack('>i',len(words))+b''.join(struct.pack('>Q',w) for w in words)
 if version>=2860:
  return comp(fields+[tag(10,'block_states',comp([tag(9,'palette',palette),tag(12,'data',packed)]))])
 return comp(fields+[tag(9,'Palette',palette),tag(12,'BlockStates',packed)])

def chunk(version,sy):
 fields=[integer('xPos',0),integer('zPos',0)]
 fields += [tag(9,'sections' if version>=2860 else 'Sections',bytes([10])+struct.pack('>i',1)+section(version,sy))]
 fields += [string('Status','minecraft:full') if version>=2860 else tag(1,'TerrainPopulated',b'\1')]
 if version<2860: fields=[tag(10,'Level',comp(fields))]
 return tag(10,'',comp([integer('DataVersion',version)]+fields))
for name,version,sy in [('1.12.2-legacy',1343,0),('1.13-dense',1519,0),('1.15-dense',2230,0),('1.16-padded',2567,0),('1.18-negative',2865,-4),('1.20.5-modern',3837,-4),('1.21.9-modern',4554,-4),('26.1-modern',4786,-4),('26.3-modern',5023,-4),('future-schema',999999,-4)]:
 (p/(name+'.nbt')).write_bytes(chunk(version,sy))
 (p/(name+'-level.nbt')).write_bytes(tag(10,'',comp([tag(10,'Data',comp([integer('DataVersion',version),tag(10,'Version',comp([string('Name',name.split('-')[0]),integer('Id',version)]))]))])))
(p/'level.nbt').write_bytes(tag(10,'',comp([tag(10,'Data',comp([integer('DataVersion',3837),tag(10,'Version',comp([string('Name','1.20.5'),integer('Id',3837)])),integer('PrivateTestSecret',123456)]))])))

# Independent primary C codec/hash oracle. Optional generation-only system libs;
# Go tests use checked-in bytes and never require native libraries or a network.
import ctypes
lz4=ctypes.CDLL('liblz4.so.1');xx=ctypes.CDLL('libxxhash.so.0')
lz4.LZ4_compressBound.argtypes=[ctypes.c_int];lz4.LZ4_compressBound.restype=ctypes.c_int
lz4.LZ4_compress_default.argtypes=[ctypes.c_void_p,ctypes.c_void_p,ctypes.c_int,ctypes.c_int];lz4.LZ4_compress_default.restype=ctypes.c_int
xx.XXH32.argtypes=[ctypes.c_void_p,ctypes.c_size_t,ctypes.c_uint];xx.XXH32.restype=ctypes.c_uint
b=(p/'1.20.5-modern.nbt').read_bytes();dst=ctypes.create_string_buffer(lz4.LZ4_compressBound(len(b)));n=lz4.LZ4_compress_default(b,dst,len(b),len(dst))
assert n>0
frame=b'LZ4Block'+bytes([0x26])+struct.pack('<III',n,len(b),xx.XXH32(b,len(b),0x9747b28c)&0x0fffffff)+dst.raw[:n]
frame+=b'LZ4Block\x16'+bytes(12)
(p/'lz4-oracle.bin').write_bytes(frame)
import json
vectors=[]
for seed in [0,0x9747b28c,0xffffffff]:
 for b in [b'',b'a',b'abc',bytes(range(256)),b'long seeded input '*1000]:vectors.append(dict(hex=b.hex(),seed=seed,sum=xx.XXH32(b,len(b),seed)))
(p/'xxhash-vectors.json').write_text(json.dumps(vectors,indent=2)+'\n')
