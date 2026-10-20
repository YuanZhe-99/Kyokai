// Kyokai's addition to RustDesk's vendored DXGI capturer (AGPL-3.0, like
// RustDesk); it reuses hdr.rs's shader plumbing.

//! Scales a desktop frame to the stream's size on the GPU, turning it the
//! right way up on the way, before it is copied to system memory: a 4K
//! desktop then crosses to the CPU as the 1080p picture the stream sends
//! (8 MB instead of 33 MB), and the CPU neither scales nor turns it — as
//! ScreenCaptureKit hands a Mac host its pictures.
//!
//! Each output pixel averages four bilinear taps around its centre. Their
//! distance grows with the scale, from none at 1:1 (each pixel is the
//! source pixel, only turned) to a quarter of an output pixel from 2:1 on:
//! an exact 2×2 average at 4K → 1080p, an exact 4×4 one at 4:1.

use super::{hdr, ComPtr};
use crate::RotationMode;
use std::{io, mem, ptr};
use winapi::{
    shared::{dxgiformat::DXGI_FORMAT_B8G8R8A8_UNORM, dxgitype::DXGI_SAMPLE_DESC},
    um::{d3d11::*, d3dcommon::D3D_PRIMITIVE_TOPOLOGY_TRIANGLELIST},
};

/// The pixel shader: `m` and `o` map the output's coordinates (0–1, the
/// picture the right way up) onto the source's (as the panel scans it),
/// `inv` is one output pixel, `tap` the taps' distance (in output
/// coordinates).
const PS_SRC: &str = "\
Texture2D<float4> src : register(t0);
SamplerState smp : register(s0);
cbuffer Params : register(b0) { float4 m; float4 o_inv; float4 tap; };
float2 at(float2 uv) { return float2(dot(m.xy, uv), dot(m.zw, uv)) + o_inv.xy; }
float4 main(float4 pos : SV_Position) : SV_Target {
    float2 uv = pos.xy * o_inv.zw;
    float3 c = src.Sample(smp, at(uv + float2(-tap.x, -tap.y))).rgb
             + src.Sample(smp, at(uv + float2( tap.x, -tap.y))).rgb
             + src.Sample(smp, at(uv + float2(-tap.x,  tap.y))).rgb
             + src.Sample(smp, at(uv + float2( tap.x,  tap.y))).rgb;
    return float4(c * 0.25, 1.0);
}";

/// The scale-and-turn pass: its shaders, sampler and parameters, and the
/// picture it renders into, kept while the size and the turn stay.
pub struct Scaler {
    device: ComPtr<ID3D11Device>,
    context: ComPtr<ID3D11DeviceContext>,
    vs: ComPtr<ID3D11VertexShader>,
    ps: ComPtr<ID3D11PixelShader>,
    sampler: ComPtr<ID3D11SamplerState>,
    params: ComPtr<ID3D11Buffer>,
    target: ComPtr<ID3D11Texture2D>,
    rtv: ComPtr<ID3D11RenderTargetView>,
    srv: ComPtr<ID3D11ShaderResourceView>,
    // Texture `srv` was made for; the view keeps it alive, so the address is
    // not reused behind our back (as in hdr.rs).
    srv_source: *mut ID3D11Texture2D,
    width: u32,
    height: u32,
    // The parameters last written to `params`.
    written: [f32; 12],
}

impl Scaler {
    /// Compiles the pass for a device.
    /// Returns: an error where Direct3D's shader compiler is missing or the
    /// device refuses a part; the capturer then scales on the CPU.
    pub fn new(device: *mut ID3D11Device, context: *mut ID3D11DeviceContext) -> io::Result<Self> {
        if device.is_null() || context.is_null() {
            return Err(io::Error::new(io::ErrorKind::Other, "no d3d11 device"));
        }
        // SAFETY: the device and context are live COM objects of the
        // capturer; each gets a reference of its own for the scaler, and
        // every object created here is checked before it is wrapped.
        unsafe {
            (*device).AddRef();
            let device = ComPtr(device);
            (*context).AddRef();
            let context = ComPtr(context);
            let compile = hdr::load_d3d_compile()?;
            let vs_code = hdr::compile_shader(compile, hdr::VS_SRC, b"vs_4_0\0")?;
            let ps_code = hdr::compile_shader(compile, PS_SRC, b"ps_4_0\0")?;
            let mut vs = ptr::null_mut();
            hdr::check(
                (*device.0).CreateVertexShader(
                    (*vs_code.0).GetBufferPointer(),
                    (*vs_code.0).GetBufferSize(),
                    ptr::null_mut(),
                    &mut vs,
                ),
                "CreateVertexShader",
            )?;
            let vs = ComPtr(vs);
            let mut ps = ptr::null_mut();
            hdr::check(
                (*device.0).CreatePixelShader(
                    (*ps_code.0).GetBufferPointer(),
                    (*ps_code.0).GetBufferSize(),
                    ptr::null_mut(),
                    &mut ps,
                ),
                "CreatePixelShader",
            )?;
            let ps = ComPtr(ps);
            let sampler_desc = D3D11_SAMPLER_DESC {
                Filter: D3D11_FILTER_MIN_MAG_MIP_LINEAR,
                AddressU: D3D11_TEXTURE_ADDRESS_CLAMP,
                AddressV: D3D11_TEXTURE_ADDRESS_CLAMP,
                AddressW: D3D11_TEXTURE_ADDRESS_CLAMP,
                MipLODBias: 0.0,
                MaxAnisotropy: 1,
                ComparisonFunc: D3D11_COMPARISON_NEVER,
                BorderColor: [0.0; 4],
                MinLOD: 0.0,
                MaxLOD: f32::MAX,
            };
            let mut sampler = ptr::null_mut();
            hdr::check(
                (*device.0).CreateSamplerState(&sampler_desc, &mut sampler),
                "CreateSamplerState",
            )?;
            let sampler = ComPtr(sampler);
            let written = [0f32; 12];
            let buffer_desc = D3D11_BUFFER_DESC {
                ByteWidth: mem::size_of_val(&written) as _,
                Usage: D3D11_USAGE_DEFAULT,
                BindFlags: D3D11_BIND_CONSTANT_BUFFER,
                CPUAccessFlags: 0,
                MiscFlags: 0,
                StructureByteStride: 0,
            };
            let data = D3D11_SUBRESOURCE_DATA {
                pSysMem: written.as_ptr() as _,
                SysMemPitch: 0,
                SysMemSlicePitch: 0,
            };
            let mut params = ptr::null_mut();
            hdr::check(
                (*device.0).CreateBuffer(&buffer_desc, &data, &mut params),
                "CreateBuffer",
            )?;
            Ok(Scaler {
                device,
                context,
                vs,
                ps,
                sampler,
                params: ComPtr(params),
                target: ComPtr(ptr::null_mut()),
                rtv: ComPtr(ptr::null_mut()),
                srv: ComPtr(ptr::null_mut()),
                srv_source: ptr::null_mut(),
                width: 0,
                height: 0,
                written,
            })
        }
    }

    /// Renders `source` (BGRA, `src_w`×`src_h` as the panel scans it)
    /// turned by `turn` and scaled to `width`×`height` into a texture of
    /// the scaler's own, and returns that texture, valid until the next
    /// call.
    /// Notes: the caller's frame stays acquired while this runs; nothing is
    /// left bound to the context afterwards.
    pub fn run(
        &mut self,
        source: *mut ID3D11Texture2D,
        (src_w, src_h): (u32, u32),
        turn: RotationMode,
        (width, height): (u32, u32),
    ) -> io::Result<*mut ID3D11Texture2D> {
        // SAFETY: `source` is the live texture the caller holds for this
        // frame; the views, the target and the parameters belong to this
        // scaler's device, and the context is used on the capturer's
        // thread only.
        unsafe {
            self.ensure_target(width, height)?;
            self.ensure_source_view(source)?;
            let params = parameters((src_w, src_h), turn, (width, height));
            if params != self.written {
                (*self.context.0).UpdateSubresource(
                    self.params.0 as *mut _,
                    0,
                    ptr::null(),
                    params.as_ptr() as _,
                    0,
                    0,
                );
                self.written = params;
            }
            let ctx = self.context.0;
            let rtv = self.rtv.0;
            let srv = self.srv.0;
            let sampler = self.sampler.0;
            let buffer = self.params.0;
            let viewport = D3D11_VIEWPORT {
                TopLeftX: 0.0,
                TopLeftY: 0.0,
                Width: width as f32,
                Height: height as f32,
                MinDepth: 0.0,
                MaxDepth: 1.0,
            };
            (*ctx).OMSetRenderTargets(1, &rtv, ptr::null_mut());
            (*ctx).OMSetBlendState(ptr::null_mut(), &[0.0; 4], 0xffff_ffff);
            (*ctx).OMSetDepthStencilState(ptr::null_mut(), 0);
            (*ctx).RSSetState(ptr::null_mut());
            (*ctx).RSSetViewports(1, &viewport);
            (*ctx).IASetInputLayout(ptr::null_mut());
            (*ctx).IASetPrimitiveTopology(D3D_PRIMITIVE_TOPOLOGY_TRIANGLELIST);
            (*ctx).VSSetShader(self.vs.0, ptr::null(), 0);
            (*ctx).PSSetShader(self.ps.0, ptr::null(), 0);
            (*ctx).PSSetConstantBuffers(0, 1, &buffer);
            (*ctx).PSSetSamplers(0, 1, &sampler);
            (*ctx).PSSetShaderResources(0, 1, &srv);
            (*ctx).Draw(3, 0);
            // Unbound, so the copy that follows never sees the target as a
            // live render target or the desktop image as a bound input.
            let no_srv: *mut ID3D11ShaderResourceView = ptr::null_mut();
            (*ctx).PSSetShaderResources(0, 1, &no_srv);
            (*ctx).OMSetRenderTargets(0, ptr::null(), ptr::null_mut());
            Ok(self.target.0)
        }
    }

    /// Makes the render target `width`×`height`, unless it is already.
    unsafe fn ensure_target(&mut self, width: u32, height: u32) -> io::Result<()> {
        if !self.target.is_null() && self.width == width && self.height == height {
            return Ok(());
        }
        let desc = D3D11_TEXTURE2D_DESC {
            Width: width,
            Height: height,
            MipLevels: 1,
            ArraySize: 1,
            Format: DXGI_FORMAT_B8G8R8A8_UNORM,
            SampleDesc: DXGI_SAMPLE_DESC {
                Count: 1,
                Quality: 0,
            },
            Usage: D3D11_USAGE_DEFAULT,
            BindFlags: D3D11_BIND_RENDER_TARGET | D3D11_BIND_SHADER_RESOURCE,
            CPUAccessFlags: 0,
            MiscFlags: 0,
        };
        let mut target = ptr::null_mut();
        hdr::check(
            (*self.device.0).CreateTexture2D(&desc, ptr::null(), &mut target),
            "CreateTexture2D",
        )?;
        let target = ComPtr(target);
        let mut rtv = ptr::null_mut();
        hdr::check(
            (*self.device.0).CreateRenderTargetView(target.0 as *mut _, ptr::null(), &mut rtv),
            "CreateRenderTargetView",
        )?;
        self.rtv = ComPtr(rtv);
        self.target = target;
        self.width = width;
        self.height = height;
        Ok(())
    }

    /// Makes the shader's view of `source`, unless it has it already.
    unsafe fn ensure_source_view(&mut self, source: *mut ID3D11Texture2D) -> io::Result<()> {
        if !self.srv.is_null() && self.srv_source == source {
            return Ok(());
        }
        let mut srv = ptr::null_mut();
        hdr::check(
            (*self.device.0).CreateShaderResourceView(source as *mut _, ptr::null(), &mut srv),
            "CreateShaderResourceView",
        )?;
        self.srv = ComPtr(srv);
        self.srv_source = source;
        Ok(())
    }
}

/// The shader's parameters for a source of `src` pixels (as the panel scans
/// it) shown turned by `turn` at `out` pixels: the turn as a matrix and an
/// offset, one output pixel, and the taps' distance.
fn parameters(src: (u32, u32), turn: RotationMode, out: (u32, u32)) -> [f32; 12] {
    // Output coordinates (u, v) the right way up → the source's, as
    // shim::ARGBRotate places each pixel.
    let (m, o): ([f32; 4], [f32; 2]) = match turn {
        RotationMode::kRotate0 => ([1.0, 0.0, 0.0, 1.0], [0.0, 0.0]),
        RotationMode::kRotate90 => ([0.0, 1.0, -1.0, 0.0], [0.0, 1.0]),
        RotationMode::kRotate180 => ([-1.0, 0.0, 0.0, -1.0], [1.0, 1.0]),
        RotationMode::kRotate270 => ([0.0, -1.0, 1.0, 0.0], [1.0, 0.0]),
    };
    // The source's size the right way up, against the output's.
    let (sw, sh) = match turn {
        RotationMode::kRotate90 | RotationMode::kRotate270 => (src.1, src.0),
        _ => src,
    };
    let (ow, oh) = (out.0.max(1) as f32, out.1.max(1) as f32);
    let ratio = (sw as f32 / ow).max(sh as f32 / oh);
    // From no spread at 1:1 to a quarter pixel at 2:1 and beyond.
    let spread = 0.25 * (ratio - 1.0).clamp(0.0, 1.0);
    [
        m[0],
        m[1],
        m[2],
        m[3],
        o[0],
        o[1],
        1.0 / ow,
        1.0 / oh,
        spread / ow,
        spread / oh,
        0.0,
        0.0,
    ]
}

#[cfg(test)]
mod tests {
    use super::*;
    use crate::shim::ARGBRotate;
    use winapi::{
        shared::{dxgiformat::DXGI_FORMAT_B8G8R8A8_UNORM, winerror::S_OK},
        um::d3dcommon::{D3D_DRIVER_TYPE, D3D_DRIVER_TYPE_HARDWARE, D3D_DRIVER_TYPE_WARP},
    };

    /// A device of the GPU, else of Windows' software rasterizer; None
    /// where neither opens.
    fn device() -> Option<(ComPtr<ID3D11Device>, ComPtr<ID3D11DeviceContext>)> {
        let open = |kind: D3D_DRIVER_TYPE| {
            let mut device = ptr::null_mut();
            let mut context = ptr::null_mut();
            // SAFETY: creates a device and its context into the two
            // pointers, checked below.
            let hr = unsafe {
                D3D11CreateDevice(
                    ptr::null_mut(),
                    kind,
                    ptr::null_mut(),
                    0,
                    ptr::null(),
                    0,
                    D3D11_SDK_VERSION,
                    &mut device,
                    ptr::null_mut(),
                    &mut context,
                )
            };
            (hr == S_OK).then(|| (ComPtr(device), ComPtr(context)))
        };
        open(D3D_DRIVER_TYPE_HARDWARE).or_else(|| open(D3D_DRIVER_TYPE_WARP))
    }

    /// A BGRA texture of `w`×`h` holding `pixels` (tightly packed).
    unsafe fn texture(
        device: &ComPtr<ID3D11Device>,
        w: u32,
        h: u32,
        pixels: &[u8],
    ) -> ComPtr<ID3D11Texture2D> {
        let desc = D3D11_TEXTURE2D_DESC {
            Width: w,
            Height: h,
            MipLevels: 1,
            ArraySize: 1,
            Format: DXGI_FORMAT_B8G8R8A8_UNORM,
            SampleDesc: DXGI_SAMPLE_DESC {
                Count: 1,
                Quality: 0,
            },
            Usage: D3D11_USAGE_DEFAULT,
            BindFlags: D3D11_BIND_SHADER_RESOURCE,
            CPUAccessFlags: 0,
            MiscFlags: 0,
        };
        let data = D3D11_SUBRESOURCE_DATA {
            pSysMem: pixels.as_ptr() as _,
            SysMemPitch: w * 4,
            SysMemSlicePitch: 0,
        };
        let mut t = ptr::null_mut();
        assert_eq!((*device.0).CreateTexture2D(&desc, &data, &mut t), S_OK);
        ComPtr(t)
    }

    /// Reads `texture` (BGRA, `w`×`h`) back, tightly packed.
    unsafe fn read(
        device: &ComPtr<ID3D11Device>,
        context: &ComPtr<ID3D11DeviceContext>,
        texture: *mut ID3D11Texture2D,
        w: u32,
        h: u32,
    ) -> Vec<u8> {
        let desc = D3D11_TEXTURE2D_DESC {
            Width: w,
            Height: h,
            MipLevels: 1,
            ArraySize: 1,
            Format: DXGI_FORMAT_B8G8R8A8_UNORM,
            SampleDesc: DXGI_SAMPLE_DESC {
                Count: 1,
                Quality: 0,
            },
            Usage: D3D11_USAGE_STAGING,
            BindFlags: 0,
            CPUAccessFlags: D3D11_CPU_ACCESS_READ,
            MiscFlags: 0,
        };
        let mut staging = ptr::null_mut();
        assert_eq!(
            (*device.0).CreateTexture2D(&desc, ptr::null(), &mut staging),
            S_OK
        );
        let staging = ComPtr(staging);
        (*context.0).CopyResource(staging.0 as *mut _, texture as *mut _);
        let mut mapped: D3D11_MAPPED_SUBRESOURCE = mem::zeroed();
        assert_eq!(
            (*context.0).Map(staging.0 as *mut _, 0, D3D11_MAP_READ, 0, &mut mapped),
            S_OK
        );
        let mut out = Vec::with_capacity((w * h * 4) as usize);
        for row in 0..h as usize {
            let line = std::slice::from_raw_parts(
                (mapped.pData as *const u8).add(row * mapped.RowPitch as usize),
                w as usize * 4,
            );
            out.extend_from_slice(line);
        }
        (*context.0).Unmap(staging.0 as *mut _, 0);
        out
    }

    /// A `w`×`h` picture whose every pixel differs (alpha opaque).
    fn pattern(w: u32, h: u32) -> Vec<u8> {
        (0..w * h)
            .flat_map(|i| {
                let i = i as u8;
                [
                    i.wrapping_mul(7),
                    i.wrapping_mul(13).wrapping_add(5),
                    i.wrapping_mul(29).wrapping_add(11),
                    255,
                ]
            })
            .collect()
    }

    /// Turning without scaling gives exactly the CPU's turned picture, for
    /// every turn.
    #[test]
    fn turns_match_the_cpu_rotation() {
        let Some((device, context)) = device() else {
            return eprintln!("skipped: no Direct3D 11 device");
        };
        let (w, h) = (6u32, 4u32);
        let src = pattern(w, h);
        // SAFETY: the textures and views belong to this test's device, used
        // on this thread only.
        unsafe {
            let source = texture(&device, w, h, &src);
            let mut scaler = match Scaler::new(device.0, context.0) {
                Ok(s) => s,
                Err(e) => return eprintln!("skipped: {e}"),
            };
            for turn in [
                RotationMode::kRotate0,
                RotationMode::kRotate90,
                RotationMode::kRotate180,
                RotationMode::kRotate270,
            ] {
                let (ow, oh) = match turn {
                    RotationMode::kRotate90 | RotationMode::kRotate270 => (h, w),
                    _ => (w, h),
                };
                let mut want = vec![0u8; (w * h * 4) as usize];
                ARGBRotate(
                    src.as_ptr(),
                    (w * 4) as i32,
                    want.as_mut_ptr(),
                    (ow * 4) as i32,
                    w as i32,
                    h as i32,
                    turn,
                );
                let out = scaler.run(source.0, (w, h), turn, (ow, oh)).expect("pass");
                let got = read(&device, &context, out, ow, oh);
                assert_eq!(got, want, "turn {}", turn as i32);
            }
        }
    }

    /// Halving gives each output pixel the average of its 2×2 source pixels
    /// (within a level of rounding), turned too.
    #[test]
    fn halving_averages_two_by_two() {
        let Some((device, context)) = device() else {
            return eprintln!("skipped: no Direct3D 11 device");
        };
        let (w, h) = (8u32, 4u32);
        let src = pattern(w, h);
        // SAFETY: as above.
        unsafe {
            let source = texture(&device, w, h, &src);
            let mut scaler = match Scaler::new(device.0, context.0) {
                Ok(s) => s,
                Err(e) => return eprintln!("skipped: {e}"),
            };
            for turn in [RotationMode::kRotate0, RotationMode::kRotate90] {
                let (tw, th) = match turn {
                    RotationMode::kRotate90 => (h, w),
                    _ => (w, h),
                };
                let mut turned = vec![0u8; (w * h * 4) as usize];
                ARGBRotate(
                    src.as_ptr(),
                    (w * 4) as i32,
                    turned.as_mut_ptr(),
                    (tw * 4) as i32,
                    w as i32,
                    h as i32,
                    turn,
                );
                let (ow, oh) = (tw / 2, th / 2);
                let out = scaler.run(source.0, (w, h), turn, (ow, oh)).expect("pass");
                let got = read(&device, &context, out, ow, oh);
                for y in 0..oh as usize {
                    for x in 0..ow as usize {
                        for c in 0..3 {
                            let px = |dx: usize, dy: usize| {
                                turned[((2 * y + dy) * tw as usize + 2 * x + dx) * 4 + c] as f32
                            };
                            let mean = (px(0, 0) + px(1, 0) + px(0, 1) + px(1, 1)) / 4.0;
                            let v = got[(y * ow as usize + x) * 4 + c] as f32;
                            assert!(
                                (v - mean).abs() <= 1.0,
                                "turn {} at {x},{y}: {v} vs {mean}",
                                turn as i32
                            );
                        }
                    }
                }
            }
        }
    }
}
