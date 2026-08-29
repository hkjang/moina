import { ArrowLeft, Home, LockKeyhole, SearchX } from 'lucide-react';
import { Link, useNavigate } from 'react-router-dom';

export function AccessDeniedPage() { return <div className="full-state"><span><LockKeyhole/></span><h1>접근 권한이 없습니다</h1><p>이 화면을 사용하려면 서비스 관리자가 역할 권한을 부여해야 합니다.</p><Link className="ui-button ui-button-primary ui-button-default" to="/flow"><Home/>플로우로 돌아가기</Link></div>; }
export function NotFoundPage() { const navigate = useNavigate(); return <div className="full-state"><span><SearchX/></span><h1>페이지를 찾을 수 없습니다</h1><p>주소가 바뀌었거나 삭제된 화면입니다.</p><div><button className="ui-button ui-button-secondary ui-button-default" type="button" onClick={() => navigate(-1)}><ArrowLeft/>이전 화면</button><Link className="ui-button ui-button-primary ui-button-default" to="/flow"><Home/>플로우</Link></div></div>; }
