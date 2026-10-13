import React, {FormEvent, useState} from 'react';
import {createRoot} from 'react-dom/client';
import './style.css';

type Source = 'javtiful' | 'sextb' | 'abxxx' | 'camstreams' | 'camwhores' | 'avjoy';
type Result = {id:string; source:string; title:string; cover?:string; type?:string};
type Detail = {id:string; source:string; title:string; description?:string; episodes?:{id:string; title:string}[]};
type Stream = {url:string; protocol:string; quality?:string};

async function api<T>(path:string, init?:RequestInit):Promise<T>{
  const response=await fetch('/api/'+path, init); const data=await response.json();
  if(!response.ok) throw new Error(data?.error?.message || '请求失败'); return data;
}

function App(){
  const [source,setSource]=useState<Source>('sextb'),[query,setQuery]=useState(''),[results,setResults]=useState<Result[]>([]),[detail,setDetail]=useState<Detail|null>(null),[streams,setStreams]=useState<Stream[]>([]),[selected,setSelected]=useState<Stream|null>(null),[busy,setBusy]=useState(false),[error,setError]=useState('');
  async function search(e?:FormEvent){e?.preventDefault();setBusy(true);setError('');setDetail(null);setStreams([]);try{const d=await api<{items:Result[]}>(`search?source=${source}&q=${encodeURIComponent(query)}&page=1`);setResults(d.items)}catch(err){setError(String(err))}finally{setBusy(false)}}
  async function open(item:Result){setBusy(true);setError('');try{setDetail(await api<Detail>(`detail/${item.source}/${item.id}`))}catch(err){setError(String(err))}finally{setBusy(false)}}
  async function resolve(id:string){setBusy(true);setError('');try{const d=await api<{streams:Stream[]}>(`resolve/${source}/${id}`,{method:'POST'});setStreams(d.streams);setSelected(d.streams[0]||null)}catch(err){setError(String(err))}finally{setBusy(false)}}
  return <><header><strong>AVXXX · 直连播放器</strong><nav><a href="/">媒体搜索</a></nav></header><main><p className="eyebrow">SEARCH · DETAIL · PLAY</p><h1>搜索并播放</h1><p className="muted">视频直接从源站加载，不经过后端代理。</p><form onSubmit={search}><select value={source} onChange={e=>setSource(e.target.value as Source)}><option value="sextb">Sextb</option><option value="javtiful">JAVtiful</option><option value="abxxx">ABxxx</option><option value="camstreams">CamStreams</option><option value="camwhores">CamWhores</option><option value="avjoy">Avjoy</option></select><input value={query} onChange={e=>setQuery(e.target.value)} placeholder="输入关键词…" required/><button disabled={busy}>搜索</button></form>{busy&&<p className="muted">处理中…</p>}{error&&<p role="alert" className="error">{error}</p>}{!detail&&<div className="grid">{results.map(item=><article key={item.id} className="result">{item.cover&&<img src={item.cover} alt="" loading="lazy"/>}<div><h2>{item.title}</h2><button onClick={()=>open(item)}>查看详情</button>{item.type==='album'&&<p className="muted">相册条目，暂无公开视频直链</p>}</div></article>)}</div>}{!detail&&query&&!busy&&!results.length&&<section className="empty">没有找到结果。</section>}{detail&&<section><button onClick={()=>setDetail(null)}>← 返回结果</button><h2>{detail.title}</h2><p>{detail.description||'暂无简介'}</p>{(detail.episodes||[]).map(ep=><button key={ep.id} onClick={()=>resolve(ep.id)}>解析播放</button>)}</section>}{streams.length>0&&<section><h2>播放源</h2><div>{streams.map((stream,i)=><button key={stream.url} onClick={()=>setSelected(stream)} className={selected?.url===stream.url?'active':''}>{stream.quality||stream.protocol||`源站 ${i+1}`}</button>)}</div>{selected&&<video key={selected.url} className="player" controls autoPlay playsInline src={selected.url}/>}<p className="muted">当前为源站直连；若浏览器无法播放，请切换其他源。</p></section>}</main></>;
}
createRoot(document.getElementById('root')!).render(<App/>);
